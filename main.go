package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// version is overridden at build time: -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	report := newReportCmd()

	root := &cobra.Command{
		Use:           "solidtime",
		Short:         "Report tracked hours and billable totals from solidtime",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		// No subcommand means "report", which is what you want 90% of the time.
		RunE: report.RunE,
	}
	root.Flags().AddFlagSet(report.Flags())

	root.AddCommand(
		report,
		newStartCmd(), newStopCmd(), newToggleCmd(), newStatusCmd(),
		newInvoiceCmd(), newSnapshotCmd(), newOrgsCmd(), newProjectsCmd(), newConfigCmd(),
	)
	return root
}

func newOrgsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "orgs",
		Short: "List the organizations you are a member of",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return fmt.Errorf("no API token: run 'solidtime config init', or set SOLIDTIME_API_TOKEN")
			}

			memberships, err := newClient(cfg.APIURL, cfg.Token).memberships()
			if err != nil {
				return err
			}

			out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(out, "ID\tNAME\tROLE")
			for _, m := range memberships {
				fmt.Fprintf(out, "%s\t%s\t%s\n", m.Organization.ID, m.Organization.Name, m.Role)
			}
			return out.Flush()
		},
	}
}

func newProjectsCmd() *cobra.Command {
	var showArchived bool
	var noColor bool
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "projects",
		Short: "List projects with their colors and billable rates",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return fmt.Errorf("no API token: run 'solidtime config init', or set SOLIDTIME_API_TOKEN")
			}

			client := newClient(cfg.APIURL, cfg.Token)
			orgID, err := resolveOrg(client, cfg.DefaultOrg)
			if err != nil {
				return err
			}
			projects, err := client.projects(orgID)
			if err != nil {
				return err
			}

			if !showArchived {
				active := projects[:0]
				for _, project := range projects {
					if !project.IsArchived {
						active = append(active, project)
					}
				}
				projects = active
			}

			if asJSON {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(projects)
			}

			color := useColor(noColor)
			out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(out, "NAME\tBILLABLE\tRATE\tID")
			for _, project := range projects {
				rate := "-"
				if project.Rate != nil {
					rate = fmt.Sprintf("%.2f", *project.Rate/100)
				}
				billable := "no"
				if project.IsBillable {
					billable = "yes"
				}
				fmt.Fprintf(out, "%s%s\t%s\t%s\t%s\n",
					colorDot(project.Color, color), project.Name, billable, rate, project.ID)
			}
			return out.Flush()
		},
	}

	cmd.Flags().BoolVar(&showArchived, "archived", false, "include archived projects")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "disable colored output")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the configuration file",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Write a starter config to ~/.config/solidtime/config.toml",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			path, err := writeDefaultConfig()
			if err != nil {
				return err
			}
			fmt.Printf("Wrote %s\nAdd your API token from the web app: Profile -> API Tokens\n", path)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the config file location",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			path, err := configPath()
			if err != nil {
				return err
			}
			fmt.Println(path)
			return nil
		},
	})

	return cmd
}
