package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
)

// Row is one rendered aggregation row, with its sub-rows when grouping two levels deep.
type Row struct {
	Label   string  `json:"label"`
	Color   string  `json:"-"`
	Seconds int     `json:"seconds"`
	Hours   float64 `json:"hours"`
	Amount  float64 `json:"amount"`
	Sub     []Row   `json:"sub,omitempty"`
}

func formatHours(seconds int) string {
	return fmt.Sprintf("%.2f", float64(seconds)/3600)
}

// colorDot renders a truecolor block for a #rrggbb project color. Empty when the
// color is missing or output is not a terminal.
func colorDot(hex string, enabled bool) string {
	if !enabled || len(hex) != 7 || hex[0] != '#' {
		return ""
	}
	r, err1 := strconv.ParseUint(hex[1:3], 16, 8)
	g, err2 := strconv.ParseUint(hex[3:5], 16, 8)
	b, err3 := strconv.ParseUint(hex[5:7], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return ""
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm●\x1b[0m ", r, g, b)
}

func useColor(noColor bool) bool {
	if noColor || os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// totalOf sums the top-level rows. Sub-rows are already counted in their parent,
// and a filtered report must total what it shows, not what the API returned.
func totalOf(rows []Row) Row {
	total := Row{Label: "TOTAL"}
	for _, row := range rows {
		total.Seconds += row.Seconds
		total.Amount += row.Amount
	}
	total.Hours = float64(total.Seconds) / 3600
	return total
}

// renderTable writes an aligned table; sub-rows are indented under their parent.
// The total row is dropped for a single-row report, where it would just repeat it.
func renderTable(w io.Writer, rows []Row, heading string, currency string, showMoney, color bool) {
	fmt.Fprintf(w, "%s\n\n", heading)

	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		writeRow(table, row, "", currency, showMoney, color)
		for _, sub := range row.Sub {
			writeRow(table, sub, "  ", currency, showMoney, color)
		}
	}

	if len(rows) > 1 {
		fmt.Fprint(table, "\t\t\n")
		writeRow(table, totalOf(rows), "", currency, showMoney, color)
	}
	table.Flush()
}

// renderPlain writes bare tab-separated values: no alignment, no color, no
// currency symbol, no header. Built for cutting up in a script or a status line.
func renderPlain(w io.Writer, rows []Row, showMoney bool) {
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s", row.Label, formatHours(row.Seconds))
		if showMoney {
			fmt.Fprintf(w, "\t%.2f", row.Amount)
		}
		fmt.Fprintln(w)
		for _, sub := range row.Sub {
			fmt.Fprintf(w, "%s\t%s", row.Label+"/"+sub.Label, formatHours(sub.Seconds))
			if showMoney {
				fmt.Fprintf(w, "\t%.2f", sub.Amount)
			}
			fmt.Fprintln(w)
		}
	}
}

func writeRow(w io.Writer, row Row, indent, currency string, showMoney, color bool) {
	fmt.Fprintf(w, "%s%s%s\t%s h", indent, colorDot(row.Color, color), row.Label, formatHours(row.Seconds))
	if showMoney {
		fmt.Fprintf(w, "\t%s%.2f", currency, row.Amount)
	}
	fmt.Fprintln(w)
}

func renderJSON(w io.Writer, rows []Row, from, to string) error {
	total := totalOf(rows)
	totalSeconds, totalAmount := total.Seconds, total.Amount
	payload := struct {
		From    string  `json:"from"`
		To      string  `json:"to"`
		Rows    []Row   `json:"rows"`
		Seconds int     `json:"total_seconds"`
		Hours   float64 `json:"total_hours"`
		Amount  float64 `json:"total_amount"`
	}{from, to, rows, totalSeconds, float64(totalSeconds) / 3600, totalAmount}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

func renderCSV(w io.Writer, rows []Row) error {
	out := csv.NewWriter(w)
	defer out.Flush()
	if err := out.Write([]string{"group", "label", "hours", "amount"}); err != nil {
		return err
	}
	for _, row := range rows {
		record := []string{"", row.Label, formatHours(row.Seconds), fmt.Sprintf("%.2f", row.Amount)}
		if err := out.Write(record); err != nil {
			return err
		}
		for _, sub := range row.Sub {
			record := []string{row.Label, sub.Label, formatHours(sub.Seconds), fmt.Sprintf("%.2f", sub.Amount)}
			if err := out.Write(record); err != nil {
				return err
			}
		}
	}
	return out.Error()
}

func renderMarkdown(w io.Writer, rows []Row, currency string, showMoney bool) {
	header := "| Project | Hours |"
	divider := "| --- | ---: |"
	if showMoney {
		header += " Amount |"
		divider += " ---: |"
	}
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, divider)

	for _, row := range rows {
		line := fmt.Sprintf("| %s | %s |", row.Label, formatHours(row.Seconds))
		if showMoney {
			line += fmt.Sprintf(" %s%.2f |", currency, row.Amount)
		}
		fmt.Fprintln(w, line)
	}

	total := totalOf(rows)
	line := fmt.Sprintf("| **Total** | **%s** |", formatHours(total.Seconds))
	if showMoney {
		line += fmt.Sprintf(" **%s%.2f** |", currency, total.Amount)
	}
	fmt.Fprintln(w, line)
}
