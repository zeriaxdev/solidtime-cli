package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const defaultAPIURL = "https://app.solidtime.io/api"

// Config is the on-disk ~/.config/solidtime/config.toml.
type Config struct {
	DefaultOrg string             `toml:"default_org"`
	APIURL     string             `toml:"api_url"`
	Token      string             `toml:"token"`
	Currency   string             `toml:"currency"`
	Rates      map[string]float64 `toml:"rates"`
	Rounding   Rounding           `toml:"rounding"`
}

// Rounding mirrors solidtime's rounding_type / rounding_minutes query params.
type Rounding struct {
	Type    string `toml:"type"`
	Minutes int    `toml:"minutes"`
}

// configPath follows XDG rather than os.UserConfigDir, which on macOS points at
// ~/Library/Application Support — not where a CLI's config belongs.
func configPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "solidtime", "config.toml"), nil
}

// loadConfig reads the config file if present, then lets env vars override it.
// A missing file is not an error: env vars alone are a valid setup.
func loadConfig() (Config, error) {
	cfg := Config{APIURL: defaultAPIURL}

	path, err := configPath()
	if err != nil {
		return cfg, err
	}
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if info.Mode().Perm()&0o077 != 0 {
			return cfg, fmt.Errorf("%s is readable by other users, run: chmod 600 %s", path, path)
		}
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return cfg, fmt.Errorf("parsing %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return cfg, err
	}

	if v := os.Getenv("SOLIDTIME_API_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("SOLIDTIME_ORGANIZATION_ID"); v != "" {
		cfg.DefaultOrg = v
	}
	if v := os.Getenv("SOLIDTIME_API_URL"); v != "" {
		cfg.APIURL = v
	}
	if cfg.APIURL == "" {
		cfg.APIURL = defaultAPIURL
	}
	cfg.APIURL = strings.TrimSuffix(cfg.APIURL, "/")
	return cfg, nil
}

// rateFor returns the flat hourly override for a project name, falling back to
// [rates].default. Zero means "use solidtime's own billable cost instead".
func (c Config) rateFor(projectName string) float64 {
	if rate, ok := c.Rates[strings.ToLower(projectName)]; ok {
		return rate
	}
	return c.Rates["default"]
}

const configTemplate = `# solidtime-cli configuration
# Environment variables override every value here:
#   SOLIDTIME_API_TOKEN, SOLIDTIME_ORGANIZATION_ID, SOLIDTIME_API_URL

# API token from the web app: Profile -> API Tokens
token = ""

# Leave empty to auto-detect from your memberships (see: solidtime orgs)
default_org = ""

# Only needed for self-hosted instances
# api_url = "https://app.solidtime.io/api"

# Overrides the symbol from your organization's currency setting.
# Leave unset to use whatever solidtime reports. Display only, converts nothing.
# currency = "€"

# Flat hourly rates, used when solidtime has no billable rate configured.
# Keys are lowercased project names; "default" applies to the rest.
# [rates]
# default = 95.0

# Rounds every entry before totaling. Premium-gated server-side.
# [rounding]
# type = "nearest"
# minutes = 15
`

func writeDefaultConfig() (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, fmt.Errorf("%s already exists, not overwriting", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(configTemplate), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
