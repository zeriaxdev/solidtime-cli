package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Invoice is a rendered document. Solidtime's API has no invoicing endpoints —
// the permissions exist but no routes do — so this is built locally from time
// entries and the rates in your config.
type Invoice struct {
	Number   string
	Issued   time.Time
	Due      time.Time
	From     time.Time
	To       time.Time
	Currency string
	Lines    []Row
	Notes    string
}

func (inv Invoice) totalHours() float64 {
	var seconds int
	for _, line := range inv.Lines {
		seconds += line.Seconds
	}
	return float64(seconds) / 3600
}

func (inv Invoice) totalAmount() float64 {
	var total float64
	for _, line := range inv.Lines {
		total += line.Amount
	}
	return total
}

type invoiceFlags struct {
	from, to string
	period   string
	group    string
	client   string
	project  string
	billable bool
	rate     float64
	round    int
	number   string
	due      int
	notes    string
	format   string
	output   string
}

func newInvoiceCmd() *cobra.Command {
	var flags invoiceFlags

	cmd := &cobra.Command{
		Use:   "invoice",
		Short: "Build an invoice from tracked time",
		Long: "Build an invoice from tracked time.\n\n" +
			"Run without flags in a terminal for a guided picker. Pass any of --period,\n" +
			"--from/--to or --client to skip it and run non-interactively.\n\n" +
			"Solidtime has no invoicing API, so the document is generated locally from\n" +
			"your time entries and the rates in your config.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInvoice(cmd, flags)
		},
	}

	cmd.Flags().StringVar(&flags.from, "from", "", "start date, YYYY-MM-DD")
	cmd.Flags().StringVar(&flags.to, "to", "", "end date, YYYY-MM-DD")
	cmd.Flags().StringVar(&flags.period, "period", "", "last-month, this-month, last-week, this-week")
	cmd.Flags().StringVar(&flags.group, "group", "project", "line items per: project, client, task, day")
	cmd.Flags().StringVar(&flags.client, "client", "", "only this client's time")
	cmd.Flags().StringVar(&flags.project, "project", "", "only projects matching this text")
	cmd.Flags().BoolVar(&flags.billable, "billable", true, "billable time only")
	cmd.Flags().Float64Var(&flags.rate, "rate", 0, "flat hourly rate, overrides configured rates")
	cmd.Flags().IntVar(&flags.round, "round", 0, "round each entry to the nearest N minutes")
	cmd.Flags().StringVar(&flags.number, "number", "", "invoice number (default: YYYY-MM based on the period)")
	cmd.Flags().IntVar(&flags.due, "due", 14, "days until payment is due")
	cmd.Flags().StringVar(&flags.notes, "notes", "", "a line of notes for the footer")
	cmd.Flags().StringVar(&flags.format, "format", "markdown", "output: markdown, html, csv")
	cmd.Flags().StringVarP(&flags.output, "output", "o", "", "write to a file instead of stdout")

	return cmd
}

// periodChoices are the presets the picker offers, in the order most people want.
var periodChoices = []choice{
	{Label: "Last month", Value: "last-month"},
	{Label: "This month", Value: "this-month"},
	{Label: "Last week", Value: "last-week"},
	{Label: "This week", Value: "this-week"},
	{Label: "Custom range…", Value: "custom"},
}

// resolveInvoiceRange turns flags, or the picker, into a date range.
func resolveInvoiceRange(flags *invoiceFlags, guided bool) (time.Time, time.Time, error) {
	if flags.from != "" || flags.to != "" {
		return resolveRange(reportFlags{from: flags.from, to: flags.to}, time.Now())
	}

	period := flags.period
	if period == "" {
		if !guided {
			return time.Time{}, time.Time{}, fmt.Errorf("pass --period or --from/--to when not running interactively")
		}
		index, err := pick("Which period?", periodChoices)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		period = periodChoices[index].Value
	}

	if period == "custom" {
		from, err := prompt("Start date (YYYY-MM-DD)?", "")
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		to, err := prompt("End date (YYYY-MM-DD)?", time.Now().Format(time.DateOnly))
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		return resolveRange(reportFlags{from: from, to: to}, time.Now())
	}

	report := reportFlags{}
	switch period {
	case "last-month":
		report.lastMonth = true
	case "this-month":
		report.thisMonth = true
	case "last-week":
		report.lastWeek = true
	case "this-week":
		report.thisWeek = true
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("--period must be one of: last-month, this-month, last-week, this-week")
	}
	return resolveRange(report, time.Now())
}

func runInvoice(cmd *cobra.Command, flags invoiceFlags) error {
	// Any explicit selection flag means the caller is scripting us.
	guided := interactive() &&
		!cmd.Flags().Changed("period") &&
		!cmd.Flags().Changed("from") &&
		!cmd.Flags().Changed("to") &&
		!cmd.Flags().Changed("client") &&
		!cmd.Flags().Changed("project")

	s, err := newSession()
	if err != nil {
		return err
	}

	start, end, err := resolveInvoiceRange(&flags, guided)
	if err != nil {
		if err == errCancelled {
			return nil
		}
		return err
	}

	if guided {
		groups := []choice{
			{Label: "One line per project", Value: "project"},
			{Label: "One line per client", Value: "client"},
			{Label: "One line per task", Value: "task"},
			{Label: "One line per day", Value: "day"},
		}
		index, err := pick("How should the lines be grouped?", groups)
		if err != nil {
			if err == errCancelled {
				return nil
			}
			return err
		}
		flags.group = groups[index].Value

		if flags.group != "client" {
			if err := pickClientFilter(s, &flags); err != nil {
				if err == errCancelled {
					return nil
				}
				return err
			}
		}
	}

	report := reportFlags{
		from:     start.Format(time.DateOnly),
		to:       end.Format(time.DateOnly),
		group:    flags.group,
		project:  flags.project,
		billable: flags.billable,
		rate:     flags.rate,
		round:    flags.round,
	}
	params := aggregateParams(report, start, end, s.cfg.Rounding)
	if flags.client != "" {
		params.Add("client_ids[]", flags.client)
	}

	aggregate, err := s.client.aggregate(s.org, params)
	if err != nil {
		return err
	}
	names, err := lookupNames(s.client, s.org, flags.group)
	if err != nil {
		return err
	}

	lines := buildRows(aggregate, names, nameMap{}, flags.project, flags.rate, s.cfg)
	if len(lines) == 0 {
		return fmt.Errorf("no billable time between %s and %s", report.from, report.to)
	}

	invoice := Invoice{
		Number:   flags.number,
		Issued:   time.Now(),
		Due:      time.Now().AddDate(0, 0, flags.due),
		From:     start,
		To:       end,
		Currency: currencySymbol(s.client, s.org, s.cfg.Currency, true),
		Lines:    lines,
		Notes:    flags.notes,
	}
	if invoice.Number == "" {
		invoice.Number = start.Format("2006-01")
	}

	rendered, err := renderInvoice(invoice, flags.format)
	if err != nil {
		return err
	}

	if flags.output == "" {
		fmt.Print(rendered)
		return nil
	}
	if err := os.WriteFile(flags.output, []byte(rendered), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%.2f h, %s%.2f)\n",
		flags.output, invoice.totalHours(), invoice.Currency, invoice.totalAmount())
	return nil
}

// pickClientFilter offers to narrow the invoice to a single client.
func pickClientFilter(s *session, flags *invoiceFlags) error {
	clients, err := s.client.named(s.org, "clients")
	if err != nil || len(clients) == 0 {
		// No clients configured is not a reason to fail an invoice.
		return nil
	}

	choices := []choice{{Label: "All clients", Value: ""}}
	for _, client := range clients {
		choices = append(choices, choice{Label: client.Name, Value: client.ID})
	}

	index, err := pick("Which client?", choices)
	if err != nil {
		return err
	}
	flags.client = choices[index].Value
	return nil
}

func renderInvoice(inv Invoice, format string) (string, error) {
	switch format {
	case "markdown", "md":
		return renderInvoiceMarkdown(inv), nil
	case "html":
		return renderInvoiceHTML(inv), nil
	case "csv":
		return renderInvoiceCSV(inv), nil
	default:
		return "", fmt.Errorf("--format must be one of: markdown, html, csv")
	}
}

func renderInvoiceMarkdown(inv Invoice) string {
	var out strings.Builder

	fmt.Fprintf(&out, "# Invoice %s\n\n", inv.Number)
	fmt.Fprintf(&out, "| | |\n| --- | --- |\n")
	fmt.Fprintf(&out, "| Issued | %s |\n", inv.Issued.Format("2 January 2006"))
	fmt.Fprintf(&out, "| Due | %s |\n", inv.Due.Format("2 January 2006"))
	fmt.Fprintf(&out, "| Period | %s – %s |\n\n",
		inv.From.Format("2 Jan 2006"), inv.To.Format("2 Jan 2006"))

	fmt.Fprintf(&out, "| Description | Hours | Amount |\n| --- | ---: | ---: |\n")
	for _, line := range inv.Lines {
		fmt.Fprintf(&out, "| %s | %.2f | %s%.2f |\n",
			line.Label, float64(line.Seconds)/3600, inv.Currency, line.Amount)
	}
	fmt.Fprintf(&out, "| **Total** | **%.2f** | **%s%.2f** |\n",
		inv.totalHours(), inv.Currency, inv.totalAmount())

	if inv.Notes != "" {
		fmt.Fprintf(&out, "\n%s\n", inv.Notes)
	}
	return out.String()
}

func renderInvoiceCSV(inv Invoice) string {
	var out strings.Builder
	fmt.Fprintf(&out, "description,hours,amount\n")
	for _, line := range inv.Lines {
		fmt.Fprintf(&out, "%q,%.2f,%.2f\n", line.Label, float64(line.Seconds)/3600, line.Amount)
	}
	fmt.Fprintf(&out, "%q,%.2f,%.2f\n", "Total", inv.totalHours(), inv.totalAmount())
	return out.String()
}

// renderInvoiceHTML is print-ready: open it in a browser and print to PDF.
func renderInvoiceHTML(inv Invoice) string {
	var rows strings.Builder
	for _, line := range inv.Lines {
		fmt.Fprintf(&rows,
			"<tr><td>%s</td><td class=n>%.2f</td><td class=n>%s%.2f</td></tr>\n",
			escapeHTML(line.Label), float64(line.Seconds)/3600, inv.Currency, line.Amount)
	}

	notes := ""
	if inv.Notes != "" {
		notes = "<p class=notes>" + escapeHTML(inv.Notes) + "</p>"
	}

	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Invoice %s</title>
<style>
  :root { color-scheme: light; }
  body { font: 15px/1.55 -apple-system, system-ui, sans-serif; color: #1a1a1a;
         max-width: 46rem; margin: 4rem auto; padding: 0 2rem; }
  h1 { font-size: 1.6rem; margin: 0 0 1.75rem; letter-spacing: -0.01em; }
  dl { display: grid; grid-template-columns: auto 1fr; gap: 0.3rem 1.5rem;
       margin: 0 0 2.5rem; font-size: 0.9rem; }
  dt { color: #6b6b6b; }
  dd { margin: 0; }
  table { width: 100%%; border-collapse: collapse; }
  th { text-align: left; font-size: 0.75rem; text-transform: uppercase;
       letter-spacing: 0.06em; color: #6b6b6b; font-weight: 600;
       padding-bottom: 0.6rem; border-bottom: 1px solid #e0e0e0; }
  td { padding: 0.65rem 0; border-bottom: 1px solid #f0f0f0; }
  .n { text-align: right; font-variant-numeric: tabular-nums; }
  tfoot td { font-weight: 600; border-bottom: none; border-top: 2px solid #1a1a1a;
             padding-top: 0.8rem; }
  .notes { margin-top: 2.5rem; color: #6b6b6b; font-size: 0.9rem; }
  @media print { body { margin: 0; padding: 0; } }
</style>
<h1>Invoice %s</h1>
<dl>
  <dt>Issued</dt><dd>%s</dd>
  <dt>Due</dt><dd>%s</dd>
  <dt>Period</dt><dd>%s &ndash; %s</dd>
</dl>
<table>
  <thead><tr><th>Description</th><th class=n>Hours</th><th class=n>Amount</th></tr></thead>
  <tbody>
%s  </tbody>
  <tfoot><tr><td>Total</td><td class=n>%.2f</td><td class=n>%s%.2f</td></tr></tfoot>
</table>
%s
`,
		escapeHTML(inv.Number), escapeHTML(inv.Number),
		inv.Issued.Format("2 January 2006"), inv.Due.Format("2 January 2006"),
		inv.From.Format("2 Jan 2006"), inv.To.Format("2 Jan 2006"),
		rows.String(), inv.totalHours(), inv.Currency, inv.totalAmount(), notes)
}

func escapeHTML(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(text)
}
