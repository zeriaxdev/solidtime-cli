package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const apiTimeFormat = "2006-01-02T15:04:05Z"

var groupTypes = []string{
	"project", "client", "task", "user", "tag", "description",
	"day", "week", "month", "year", "billable",
}

type reportFlags struct {
	from, to  string
	lastMonth bool
	thisMonth bool
	lastWeek  bool
	thisWeek  bool
	yesterday bool
	today     bool
	group     string
	subGroup  string
	project   string
	billable  bool
	rate      float64
	round     int
	format    string
	total     bool
	noColor   bool
}

// resolveRange turns the date flags into an inclusive [start, end] day range.
// Exactly one shortcut may be set; --from/--to must be given together.
func resolveRange(f reportFlags, now time.Time) (time.Time, time.Time, error) {
	day := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	today := day(now)

	if f.from != "" || f.to != "" {
		if f.from == "" || f.to == "" {
			return time.Time{}, time.Time{}, fmt.Errorf("--from and --to must be given together")
		}
		start, err := time.ParseInLocation("2006-01-02", f.from, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--from: %w", err)
		}
		end, err := time.ParseInLocation("2006-01-02", f.to, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--to: %w", err)
		}
		if end.Before(start) {
			return time.Time{}, time.Time{}, fmt.Errorf("--to is before --from")
		}
		return start, end, nil
	}

	// Weeks start Monday, matching solidtime's default.
	weekday := (int(today.Weekday()) + 6) % 7

	firstOfThisMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)

	switch {
	case f.yesterday:
		return today.AddDate(0, 0, -1), today.AddDate(0, 0, -1), nil
	case f.thisWeek:
		return today.AddDate(0, 0, -weekday), today, nil
	case f.lastWeek:
		start := today.AddDate(0, 0, -weekday-7)
		return start, start.AddDate(0, 0, 6), nil
	case f.thisMonth:
		return firstOfThisMonth, today, nil
	case f.lastMonth:
		end := firstOfThisMonth.AddDate(0, 0, -1)
		return time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC), end, nil
	default:
		// Bare `solidtime` answers "what did I do today".
		return today, today, nil
	}
}

func aggregateParams(f reportFlags, start, end time.Time, rounding Rounding) url.Values {
	params := url.Values{
		"group": {f.group},
		"start": {start.Format(apiTimeFormat)},
		"end":   {end.Add(24*time.Hour - time.Second).Format(apiTimeFormat)},
	}
	if f.subGroup != "" {
		params.Set("sub_group", f.subGroup)
	}
	if f.billable {
		params.Set("billable", "true")
	}
	minutes, roundType := rounding.Minutes, rounding.Type
	if f.round > 0 {
		minutes, roundType = f.round, "nearest"
	}
	if minutes > 0 && roundType != "" {
		params.Set("rounding_type", roundType)
		params.Set("rounding_minutes", strconv.Itoa(minutes))
	}
	return params
}

// nameMap resolves the UUID keys of a grouping to display names and colors.
// The aggregate endpoint returns null descriptions, so we build this ourselves.
type nameMap map[string]Named

func lookupNames(client *Client, orgID, groupType string) (nameMap, error) {
	out := nameMap{}

	switch groupType {
	case "project":
		projects, err := client.projects(orgID)
		if err != nil {
			return nil, err
		}
		for _, project := range projects {
			out[project.ID] = Named{ID: project.ID, Name: project.Name, Color: project.Color}
		}
	case "client", "task", "tag":
		items, err := client.named(orgID, groupType+"s")
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			out[item.ID] = item
		}
	default:
		// day/week/month/description/billable already carry a human-readable key.
		return out, nil
	}
	return out, nil
}

// label turns a group key into something readable, falling back to the raw key
// so an unresolvable UUID is still visible rather than silently blank.
func (n nameMap) label(key *string, groupType string) (string, string) {
	if key == nil || *key == "" {
		return emptyLabel(groupType), ""
	}
	if named, ok := n[*key]; ok {
		return named.Name, named.Color
	}
	return *key, ""
}

func emptyLabel(groupType string) string {
	switch groupType {
	case "project", "client", "task", "tag":
		return "(no " + groupType + ")"
	case "description":
		return "(no description)"
	default:
		return "(none)"
	}
}

// buildRows converts an Aggregate into renderable rows, resolving names and money.
func buildRows(agg Aggregate, names, subNames nameMap, filter string, rate float64, cfg Config) []Row {
	rows := make([]Row, 0, len(agg.GroupedData))

	for _, group := range agg.GroupedData {
		name, color := names.label(group.Key, agg.GroupedType)
		if filter != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(filter)) {
			continue
		}

		row := Row{
			Label:   name,
			Color:   color,
			Seconds: group.Seconds,
			Hours:   float64(group.Seconds) / 3600,
			Amount:  amountFor(group.Seconds, group.Cost, rate, name, cfg),
		}
		for _, sub := range group.GroupedData {
			subName, subColor := subNames.label(sub.Key, group.GroupedType)
			row.Sub = append(row.Sub, Row{
				Label:   subName,
				Color:   subColor,
				Seconds: sub.Seconds,
				Hours:   float64(sub.Seconds) / 3600,
				Amount:  amountFor(sub.Seconds, sub.Cost, rate, subName, cfg),
			})
		}
		rows = append(rows, row)
	}

	sortRowsBySeconds(rows)
	return rows
}

// amountFor prefers an explicit --rate, then a configured per-project rate, then
// solidtime's own billable cost (integer cents, null when rates are hidden).
func amountFor(seconds int, cost *int, rate float64, name string, cfg Config) float64 {
	hours := float64(seconds) / 3600
	if rate > 0 {
		return hours * rate
	}
	if configured := cfg.rateFor(strings.ToLower(name)); configured > 0 {
		return hours * configured
	}
	if cost == nil {
		return 0
	}
	return float64(*cost) / 100
}

func sortRowsBySeconds(rows []Row) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].Seconds > rows[j-1].Seconds; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	for i := range rows {
		if len(rows[i].Sub) > 1 {
			sortRowsBySeconds(rows[i].Sub)
		}
	}
}

func newReportCmd() *cobra.Command {
	var flags reportFlags

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Show tracked hours and billable totals for a date range",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReport(flags)
		},
	}

	cmd.Flags().StringVar(&flags.from, "from", "", "start date, YYYY-MM-DD")
	cmd.Flags().StringVar(&flags.to, "to", "", "end date, YYYY-MM-DD")
	cmd.Flags().BoolVar(&flags.today, "today", false, "today only (the default)")
	cmd.Flags().BoolVar(&flags.yesterday, "yesterday", false, "yesterday only")
	cmd.Flags().BoolVar(&flags.thisWeek, "this-week", false, "this week so far, from Monday")
	cmd.Flags().BoolVar(&flags.lastWeek, "last-week", false, "previous week, Monday to Sunday")
	cmd.Flags().BoolVar(&flags.thisMonth, "this-month", false, "this calendar month so far")
	cmd.Flags().BoolVar(&flags.lastMonth, "last-month", false, "previous calendar month")
	cmd.Flags().StringVar(&flags.group, "group", "project", "group by: "+strings.Join(groupTypes, ", "))
	cmd.Flags().StringVar(&flags.subGroup, "sub-group", "", "second grouping level, same values as --group")
	cmd.Flags().StringVar(&flags.project, "project", "", "only rows whose name contains this text")
	cmd.Flags().BoolVar(&flags.billable, "billable", false, "billable time only")
	cmd.Flags().Float64Var(&flags.rate, "rate", 0, "flat hourly rate, overrides configured rates")
	cmd.Flags().IntVar(&flags.round, "round", 0, "round each entry to the nearest N minutes")
	cmd.Flags().StringVar(&flags.format, "format", "table", "output: table, plain, json, csv, markdown")
	cmd.Flags().BoolVar(&flags.total, "total", false, "print only the total, no per-group rows")
	cmd.Flags().BoolVar(&flags.noColor, "no-color", false, "disable colored output")

	return cmd
}

func runReport(flags reportFlags) error {
	if !contains(groupTypes, flags.group) {
		return fmt.Errorf("--group must be one of: %s", strings.Join(groupTypes, ", "))
	}
	if flags.subGroup != "" && !contains(groupTypes, flags.subGroup) {
		return fmt.Errorf("--sub-group must be one of: %s", strings.Join(groupTypes, ", "))
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.Token == "" {
		return fmt.Errorf("no API token: run 'solidtime config init', or set SOLIDTIME_API_TOKEN")
	}

	start, end, err := resolveRange(flags, time.Now())
	if err != nil {
		return err
	}

	client := newClient(cfg.APIURL, cfg.Token)
	orgID, err := resolveOrg(client, cfg.DefaultOrg)
	if err != nil {
		return err
	}

	agg, err := client.aggregate(orgID, aggregateParams(flags, start, end, cfg.Rounding))
	if err != nil {
		return err
	}

	names, err := lookupNames(client, orgID, flags.group)
	if err != nil {
		return err
	}
	subNames := nameMap{}
	if flags.subGroup != "" {
		if subNames, err = lookupNames(client, orgID, flags.subGroup); err != nil {
			return err
		}
	}

	rows := buildRows(agg, names, subNames, flags.project, flags.rate, cfg)
	if len(rows) == 0 {
		fmt.Printf("No time entries between %s and %s.\n", start.Format(time.DateOnly), end.Format(time.DateOnly))
		return nil
	}

	showMoney := anyAmount(rows)
	currency := currencySymbol(client, orgID, cfg.Currency, showMoney)

	// --total collapses to a single summed row, so every format keeps working.
	if flags.total {
		rows = []Row{totalOf(rows)}
	}

	switch flags.format {
	case "json":
		return renderJSON(os.Stdout, rows, start.Format(time.DateOnly), end.Format(time.DateOnly))
	case "csv":
		return renderCSV(os.Stdout, rows)
	case "plain":
		renderPlain(os.Stdout, rows, showMoney)
		return nil
	case "markdown", "md":
		renderMarkdown(os.Stdout, rows, currency, showMoney)
		return nil
	case "table":
		heading := fmt.Sprintf("%s .. %s", start.Format(time.DateOnly), end.Format(time.DateOnly))
		renderTable(os.Stdout, rows, heading, currency, showMoney, useColor(flags.noColor))
		return nil
	default:
		return fmt.Errorf("--format must be one of: table, plain, json, csv, markdown")
	}
}

// currencySymbol prefers an explicit config override, otherwise asks the org.
// A failed lookup falls back to the currency code being blank rather than
// erroring: a wrong symbol should not cost you the whole report.
func currencySymbol(client *Client, orgID, override string, needed bool) string {
	if override != "" {
		return override
	}
	if !needed {
		return ""
	}
	org, err := client.organization(orgID)
	if err != nil {
		return ""
	}
	if org.CurrencySymbol != "" {
		return org.CurrencySymbol
	}
	if org.Currency != "" {
		return org.Currency + " "
	}
	return ""
}

func anyAmount(rows []Row) bool {
	for _, row := range rows {
		if row.Amount > 0 {
			return true
		}
	}
	return false
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
