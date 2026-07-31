package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// session bundles the three things every timer command needs, so each one does
// not repeat the config-token-org dance.
type session struct {
	client *Client
	org    string
	cfg    Config
}

func newSession() (*session, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("no API token: run 'solidtime config init', or set SOLIDTIME_API_TOKEN")
	}
	client := newClient(cfg.APIURL, cfg.Token)
	org, err := resolveOrg(client, cfg.DefaultOrg)
	if err != nil {
		return nil, err
	}
	return &session{client: client, org: org, cfg: cfg}, nil
}

// findProject matches a project by case-insensitive substring. An ambiguous
// match is an error rather than a coin flip, since it decides what gets billed.
func (s *session) findProject(needle string) (Project, error) {
	projects, err := s.client.projects(s.org)
	if err != nil {
		return Project{}, err
	}

	var matches []Project
	for _, project := range projects {
		if project.IsArchived {
			continue
		}
		if strings.EqualFold(project.Name, needle) {
			return project, nil
		}
		if strings.Contains(strings.ToLower(project.Name), strings.ToLower(needle)) {
			matches = append(matches, project)
		}
	}

	switch len(matches) {
	case 0:
		return Project{}, fmt.Errorf("no project matches %q, see 'solidtime projects'", needle)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.Name)
		}
		return Project{}, fmt.Errorf("%q matches several projects: %s", needle, strings.Join(names, ", "))
	}
}

// elapsed is how long a running entry has been going.
func elapsed(entry TimeEntry, now time.Time) time.Duration {
	start, err := time.Parse(apiTimeFormat, entry.Start)
	if err != nil {
		return 0
	}
	return now.UTC().Sub(start)
}

// formatDuration renders a duration as 1:05, the shape a status line wants.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// describe names a running entry: its description, else its project, else a placeholder.
func (s *session) describe(entry TimeEntry) string {
	if entry.Description != nil && *entry.Description != "" {
		return *entry.Description
	}
	if entry.ProjectID != nil {
		if projects, err := s.client.projects(s.org); err == nil {
			for _, project := range projects {
				if project.ID == *entry.ProjectID {
					return project.Name
				}
			}
		}
	}
	return "(no description)"
}

func newStartCmd() *cobra.Command {
	var projectName string
	var billable bool
	var force bool

	cmd := &cobra.Command{
		Use:   "start [description]",
		Short: "Start the timer",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			s, err := newSession()
			if err != nil {
				return err
			}

			running, err := s.client.activeEntry()
			if err != nil {
				return err
			}
			if running != nil {
				if !force {
					return fmt.Errorf("a timer is already running: %s (%s). Use 'solidtime stop' first, or --force to switch",
						s.describe(*running), formatDuration(elapsed(*running, time.Now())))
				}
				if _, err := s.client.stopEntry(s.org, running.ID, time.Now()); err != nil {
					return err
				}
			}

			user, err := s.client.me()
			if err != nil {
				return err
			}
			memberID, err := s.client.memberID(s.org, user.ID)
			if err != nil {
				return err
			}

			entry := map[string]any{
				"member_id": memberID,
				"start":     time.Now().UTC().Format(apiTimeFormat),
				"billable":  billable,
			}
			if description := strings.Join(args, " "); description != "" {
				entry["description"] = description
			}
			if projectName != "" {
				project, err := s.findProject(projectName)
				if err != nil {
					return err
				}
				entry["project_id"] = project.ID
				// A project marked billable should bill by default, without the flag.
				if !billable && project.IsBillable {
					entry["billable"] = true
				}
			}

			started, err := s.client.startEntry(s.org, entry)
			if err != nil {
				return err
			}
			fmt.Printf("Started %s\n", s.describe(started))
			return nil
		},
	}

	cmd.Flags().StringVarP(&projectName, "project", "p", "", "project name or a unique part of it")
	cmd.Flags().BoolVarP(&billable, "billable", "b", false, "mark the entry billable")
	cmd.Flags().BoolVar(&force, "force", false, "stop a running timer first instead of failing")
	return cmd
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running timer",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			s, err := newSession()
			if err != nil {
				return err
			}
			running, err := s.client.activeEntry()
			if err != nil {
				return err
			}
			if running == nil {
				fmt.Println("No timer is running.")
				return nil
			}

			name := s.describe(*running)
			took := elapsed(*running, time.Now())
			if _, err := s.client.stopEntry(s.org, running.ID, time.Now()); err != nil {
				return err
			}
			fmt.Printf("Stopped %s after %s\n", name, formatDuration(took))
			return nil
		},
	}
}

func newToggleCmd() *cobra.Command {
	var projectName string
	var billable bool

	cmd := &cobra.Command{
		Use:   "toggle [description]",
		Short: "Stop the timer if it is running, otherwise start it",
		Long:  "Stop the timer if it is running, otherwise start it. Built for a single button or hotkey.",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newSession()
			if err != nil {
				return err
			}
			running, err := s.client.activeEntry()
			if err != nil {
				return err
			}
			if running != nil {
				return newStopCmd().RunE(cmd, nil)
			}

			start := newStartCmd()
			if projectName != "" {
				if err := start.Flags().Set("project", projectName); err != nil {
					return err
				}
			}
			if billable {
				if err := start.Flags().Set("billable", "true"); err != nil {
					return err
				}
			}
			return start.RunE(cmd, args)
		},
	}

	cmd.Flags().StringVarP(&projectName, "project", "p", "", "project to start with, when starting")
	cmd.Flags().BoolVarP(&billable, "billable", "b", false, "mark the entry billable, when starting")
	return cmd
}

func newStatusCmd() *cobra.Command {
	var short bool
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the running timer, if any",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			s, err := newSession()
			if err != nil {
				return err
			}
			running, err := s.client.activeEntry()
			if err != nil {
				return err
			}

			if asJSON {
				return writeStatusJSON(os.Stdout, s, running)
			}

			if running == nil {
				if short {
					// Empty output keeps a status line from showing a stale label.
					return nil
				}
				fmt.Println("No timer is running.")
				return nil
			}

			since := formatDuration(elapsed(*running, time.Now()))
			if short {
				fmt.Printf("%s %s\n", since, s.describe(*running))
				return nil
			}
			fmt.Printf("%s\nrunning for %s\n", s.describe(*running), since)
			return nil
		},
	}

	// A status line polls this constantly; --short keeps it to one terse line.
	cmd.Flags().BoolVar(&short, "short", false, "one line, empty when nothing is running")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable status, for scripts and menu bar apps")
	return cmd
}

// StatusJSON is the stable shape consumers parse. Start is included so a caller
// can tick its own clock instead of polling this command every second.
type StatusJSON struct {
	Running     bool   `json:"running"`
	ID          string `json:"id,omitempty"`
	Description string `json:"description,omitempty"`
	Project     string `json:"project,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	Color       string `json:"color,omitempty"`
	Billable    bool   `json:"billable,omitempty"`
	Start       string `json:"start,omitempty"`
	Seconds     int    `json:"seconds,omitempty"`
}

func writeStatusJSON(w io.Writer, s *session, running *TimeEntry) error {
	status := StatusJSON{}
	if running != nil {
		status = StatusJSON{
			Running:  true,
			ID:       running.ID,
			Billable: running.Billable,
			Start:    running.Start,
			Seconds:  int(elapsed(*running, time.Now()).Seconds()),
		}
		if running.Description != nil {
			status.Description = *running.Description
		}
		if running.ProjectID != nil {
			status.ProjectID = *running.ProjectID
			if projects, err := s.client.projects(s.org); err == nil {
				for _, project := range projects {
					if project.ID == *running.ProjectID {
						status.Project, status.Color = project.Name, project.Color
					}
				}
			}
		}
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(status)
}
