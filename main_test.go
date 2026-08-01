package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestResolveRange(t *testing.T) {
	// A Wednesday, so weekday shortcuts have something to bite on.
	now := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		flags       reportFlags
		start, end  string
		expectError bool
	}{
		{name: "default is today", start: "2026-07-15", end: "2026-07-15"},
		{name: "last month", flags: reportFlags{lastMonth: true}, start: "2026-06-01", end: "2026-06-30"},
		{name: "this month", flags: reportFlags{thisMonth: true}, start: "2026-07-01", end: "2026-07-15"},
		{name: "today", flags: reportFlags{today: true}, start: "2026-07-15", end: "2026-07-15"},
		{name: "yesterday", flags: reportFlags{yesterday: true}, start: "2026-07-14", end: "2026-07-14"},
		{name: "this week starts monday", flags: reportFlags{thisWeek: true}, start: "2026-07-13", end: "2026-07-15"},
		{name: "last week is a full week", flags: reportFlags{lastWeek: true}, start: "2026-07-06", end: "2026-07-12"},
		{
			name:  "explicit range",
			flags: reportFlags{from: "2026-05-01", to: "2026-05-31"},
			start: "2026-05-01", end: "2026-05-31",
		},
		{name: "from without to", flags: reportFlags{from: "2026-05-01"}, expectError: true},
		{name: "to before from", flags: reportFlags{from: "2026-05-31", to: "2026-05-01"}, expectError: true},
		{name: "unparseable date", flags: reportFlags{from: "01/05/2026", to: "2026-05-31"}, expectError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := resolveRange(tc.flags, now)
			if tc.expectError {
				if err == nil {
					t.Fatal("expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := start.Format(time.DateOnly); got != tc.start {
				t.Errorf("start = %s, want %s", got, tc.start)
			}
			if got := end.Format(time.DateOnly); got != tc.end {
				t.Errorf("end = %s, want %s", got, tc.end)
			}
		})
	}
}

func TestResolveRangeAcrossYearBoundary(t *testing.T) {
	now := time.Date(2027, 1, 10, 0, 0, 0, 0, time.UTC)
	start, end, err := resolveRange(reportFlags{lastMonth: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if start.Format(time.DateOnly) != "2026-12-01" || end.Format(time.DateOnly) != "2026-12-31" {
		t.Fatalf("got %s..%s, want 2026-12-01..2026-12-31", start.Format(time.DateOnly), end.Format(time.DateOnly))
	}
}

func TestAggregateParams(t *testing.T) {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	params := aggregateParams(reportFlags{group: "project", billable: true}, start, end, Rounding{})
	if got := params.Get("start"); got != "2026-06-01T00:00:00Z" {
		t.Errorf("start = %q", got)
	}
	// The end date must cover the whole final day, or you silently lose a day of work.
	if got := params.Get("end"); got != "2026-06-30T23:59:59Z" {
		t.Errorf("end = %q, want the last second of the day", got)
	}
	if got := params.Get("billable"); got != "true" {
		t.Errorf("billable = %q", got)
	}
	if params.Has("rounding_type") {
		t.Error("rounding must stay unset when neither flag nor config asks for it")
	}

	rounded := aggregateParams(reportFlags{group: "project", round: 15}, start, end, Rounding{})
	if rounded.Get("rounding_type") != "nearest" || rounded.Get("rounding_minutes") != "15" {
		t.Errorf("--round did not set both rounding params: %v", rounded)
	}

	fromConfig := aggregateParams(reportFlags{group: "project"}, start, end, Rounding{Type: "up", Minutes: 30})
	if fromConfig.Get("rounding_type") != "up" || fromConfig.Get("rounding_minutes") != "30" {
		t.Errorf("config rounding ignored: %v", fromConfig)
	}
}

func TestAmountFor(t *testing.T) {
	cfg := Config{Rates: map[string]float64{"default": 50, "website": 120}}
	hour := 3600

	tests := []struct {
		name string
		cost *int
		rate float64
		proj string
		cfg  Config
		want float64
	}{
		{name: "solidtime cost in cents", cost: ptr(855000), want: 8550},
		{name: "flag rate wins over cost", cost: ptr(855000), rate: 100, want: 100},
		{name: "config project rate beats default", cost: nil, proj: "website", cfg: cfg, want: 120},
		{name: "config default rate", cost: nil, proj: "something else", cfg: cfg, want: 50},
		{name: "flag beats config", cost: nil, rate: 10, proj: "website", cfg: cfg, want: 10},
		{name: "null cost with no rates is zero", cost: nil, want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := amountFor(hour, tc.cost, tc.rate, tc.proj, tc.cfg); got != tc.want {
				t.Errorf("amountFor = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildRowsResolvesNamesAndSorts(t *testing.T) {
	agg := Aggregate{
		GroupedType: "project",
		GroupedData: []Group{
			{Key: ptr("p1"), Seconds: 5400, Cost: ptr(855000)},
			{Key: ptr("p2"), Seconds: 7200, Cost: nil},
			{Key: nil, Seconds: 60},
			{Key: ptr("unknown-uuid"), Seconds: 30},
		},
	}
	names := nameMap{
		"p1": {ID: "p1", Name: "Website", Color: "#ff0000"},
		"p2": {ID: "p2", Name: "Other", Color: "#00ff00"},
	}

	rows := buildRows(agg, names, nameMap{}, "", 0, Config{})
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}
	if rows[0].Label != "Other" || rows[1].Label != "Website" {
		t.Errorf("rows not sorted by time desc: %s, %s", rows[0].Label, rows[1].Label)
	}
	if rows[1].Color != "#ff0000" {
		t.Errorf("color not resolved: %q", rows[1].Color)
	}
	if rows[2].Label != "(no project)" {
		t.Errorf("null key label = %q", rows[2].Label)
	}
	// An unresolvable id must stay visible rather than render as an empty row.
	if rows[3].Label != "unknown-uuid" {
		t.Errorf("unknown key label = %q, want the raw key", rows[3].Label)
	}
	if rows[1].Amount != 8550 {
		t.Errorf("amount = %v, want 8550", rows[1].Amount)
	}
}

func TestBuildRowsFilterAndSubGroups(t *testing.T) {
	agg := Aggregate{
		GroupedType: "client",
		GroupedData: []Group{
			{
				Key: ptr("c1"), Seconds: 9000, GroupedType: "project",
				GroupedData: []Group{
					{Key: ptr("p1"), Seconds: 3600},
					{Key: ptr("p2"), Seconds: 5400},
				},
			},
			{Key: ptr("c2"), Seconds: 1800},
		},
	}
	clients := nameMap{"c1": {Name: "Acme"}, "c2": {Name: "Globex"}}
	projects := nameMap{"p1": {Name: "Alpha"}, "p2": {Name: "Beta"}}

	rows := buildRows(agg, clients, projects, "acme", 0, Config{})
	if len(rows) != 1 || rows[0].Label != "Acme" {
		t.Fatalf("filter did not match case-insensitively: %+v", rows)
	}
	if len(rows[0].Sub) != 2 {
		t.Fatalf("got %d sub-rows, want 2", len(rows[0].Sub))
	}
	if rows[0].Sub[0].Label != "Beta" {
		t.Errorf("sub-rows not sorted by time desc: %s", rows[0].Sub[0].Label)
	}
}

func TestRenderTableTotalsOnlyVisibleRows(t *testing.T) {
	rows := []Row{
		{Label: "Alpha", Seconds: 3600, Amount: 100},
		{Label: "Beta", Seconds: 1800, Amount: 50},
	}
	var buf bytes.Buffer
	renderTable(&buf, rows, "2026-07-01 .. 2026-07-31", "$", true, false)

	out := buf.String()
	if !strings.Contains(out, "1.50 h") {
		t.Errorf("total hours missing from:\n%s", out)
	}
	if !strings.Contains(out, "$150.00") {
		t.Errorf("total amount missing from:\n%s", out)
	}
}

func TestRenderPlainIsUnstyled(t *testing.T) {
	rows := []Row{
		{Label: "Acme", Seconds: 3600, Amount: 120, Color: "#ff0000", Sub: []Row{
			{Label: "Alpha", Seconds: 3600, Amount: 120},
		}},
	}
	var buf bytes.Buffer
	renderPlain(&buf, rows, true)

	want := "Acme\t1.00\t120.00\nAcme/Alpha\t1.00\t120.00\n"
	if got := buf.String(); got != want {
		t.Errorf("renderPlain =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(buf.String(), "\x1b") {
		t.Error("plain output must never contain escape codes")
	}
}

func TestTotalOfSumsOnlyTopLevel(t *testing.T) {
	rows := []Row{
		{Label: "Acme", Seconds: 9000, Amount: 300, Sub: []Row{
			{Label: "Alpha", Seconds: 3600, Amount: 120},
			{Label: "Beta", Seconds: 5400, Amount: 180},
		}},
		{Label: "Globex", Seconds: 1800, Amount: 60},
	}
	// Sub-rows are already inside their parent; counting them would double the invoice.
	if got := totalOf(rows); got.Seconds != 10800 || got.Amount != 360 || got.Hours != 3 {
		t.Errorf("totalOf = %+v, want 10800s / 360 / 3h", got)
	}
}

func TestRenderTableSkipsRedundantTotal(t *testing.T) {
	var buf bytes.Buffer
	renderTable(&buf, []Row{{Label: "Alpha", Seconds: 3600}}, "heading", "€", false, false)
	if strings.Contains(buf.String(), "TOTAL") {
		t.Errorf("single-row report should not repeat itself as a total:\n%s", buf.String())
	}
}

func TestRenderJSONTotals(t *testing.T) {
	rows := []Row{{Label: "Alpha", Seconds: 5400, Hours: 1.5, Amount: 120}}
	var buf bytes.Buffer
	if err := renderJSON(&buf, rows, "2026-07-01", "2026-07-31"); err != nil {
		t.Fatal(err)
	}

	var out struct {
		TotalSeconds int     `json:"total_seconds"`
		TotalHours   float64 `json:"total_hours"`
		TotalAmount  float64 `json:"total_amount"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.TotalSeconds != 5400 || out.TotalHours != 1.5 || out.TotalAmount != 120 {
		t.Errorf("bad totals: %+v", out)
	}
}

func TestColorDot(t *testing.T) {
	if got, want := colorDot("#ff8800", true), "\x1b[38;2;255;136;0m●\x1b[0m "; got != want {
		t.Errorf("colorDot = %q, want %q", got, want)
	}
	if got := colorDot("#ff8800", false); got != "" {
		t.Errorf("color disabled should render nothing, got %q", got)
	}
	if got := colorDot("nonsense", true); got != "" {
		t.Errorf("malformed hex should render nothing, got %q", got)
	}
}

// TestClientEndToEnd runs the real client against a fake solidtime, so a change to
// the URL shape or the "data" envelope fails here rather than in your terminal.
func TestClientEndToEnd(t *testing.T) {
	var gotPath, gotQuery, gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case strings.HasSuffix(r.URL.Path, "/time-entries/aggregate"):
			gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
			w.Write([]byte(`{"data":{"seconds":12600,"cost":null,"grouped_type":"project",
				"grouped_data":[{"key":"p1","seconds":9000,"cost":855000,"grouped_type":null,"grouped_data":null},
				               {"key":"p2","seconds":3600,"cost":null,"grouped_type":null,"grouped_data":null}]}}`))
		case strings.HasSuffix(r.URL.Path, "/projects"):
			w.Write([]byte(`{"data":[{"id":"p1","name":"Website","color":"#ff0000","is_billable":true},
			                        {"id":"p2","name":"Other","color":"#00ff00","is_billable":false}]}`))
		default:
			http.Error(w, `{"message":"API resource not found"}`, http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newClient(server.URL, "test-token")
	params := aggregateParams(
		reportFlags{group: "project"},
		time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		Rounding{},
	)

	agg, err := client.aggregate("org-uuid", params)
	if err != nil {
		t.Fatal(err)
	}
	// Plural "organizations" is the whole reason the first version 404'd.
	if gotPath != "/v1/organizations/org-uuid/time-entries/aggregate" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "group=project") {
		t.Errorf("query = %q", gotQuery)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("auth header = %q", gotAuth)
	}

	names, err := lookupNames(client, "org-uuid", "project")
	if err != nil {
		t.Fatal(err)
	}
	rows := buildRows(agg, names, nameMap{}, "", 0, Config{})
	if len(rows) != 2 || rows[0].Label != "Website" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	if rows[0].Amount != 8550 {
		t.Errorf("amount = %v, want 8550", rows[0].Amount)
	}
}

func TestCurrencySymbol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"id":"org","name":"Acme","currency":"EUR","currency_symbol":"€"}}`))
	}))
	defer server.Close()
	client := newClient(server.URL, "t")

	if got := currencySymbol(client, "org", "", true); got != "€" {
		t.Errorf("symbol = %q, want the org's €", got)
	}
	if got := currencySymbol(client, "org", "kr", true); got != "kr" {
		t.Errorf("config override ignored: %q", got)
	}

	// No money column means no reason to spend a request on the org.
	var called bool
	quiet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Write([]byte(`{"data":{}}`))
	}))
	defer quiet.Close()
	if got := currencySymbol(newClient(quiet.URL, "t"), "org", "", false); got != "" || called {
		t.Errorf("symbol = %q, org fetched = %v; want no fetch", got, called)
	}

	// A broken org lookup must not take the report down with it.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"nope"}`, http.StatusForbidden)
	}))
	defer broken.Close()
	if got := currencySymbol(newClient(broken.URL, "t"), "org", "", true); got != "" {
		t.Errorf("failed lookup should degrade to no symbol, got %q", got)
	}
}

func TestElapsedAndFormatDuration(t *testing.T) {
	now := time.Date(2026, 7, 15, 14, 30, 0, 0, time.UTC)
	entry := TimeEntry{Start: "2026-07-15T13:25:00Z"}

	if got := elapsed(entry, now); got != 65*time.Minute {
		t.Errorf("elapsed = %v, want 65m", got)
	}
	if got := formatDuration(elapsed(entry, now)); got != "1:05" {
		t.Errorf("formatDuration = %q, want 1:05", got)
	}
	// An unparseable start must not render as a wild duration.
	if got := elapsed(TimeEntry{Start: "nonsense"}, now); got != 0 {
		t.Errorf("elapsed on bad start = %v, want 0", got)
	}
	// Clock skew between machine and server should floor at zero, not go negative.
	if got := formatDuration(-5 * time.Minute); got != "0:00" {
		t.Errorf("negative duration = %q, want 0:00", got)
	}
}

func TestFindProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[
			{"id":"p1","name":"Website","color":"#ff0000","is_billable":true},
			{"id":"p2","name":"Website Redesign","color":"#00ff00","is_billable":false},
			{"id":"p3","name":"Internal","color":"#0000ff","is_billable":false},
			{"id":"p4","name":"Old Website","color":"#ffffff","is_archived":true}
		]}`))
	}))
	defer server.Close()
	s := &session{client: newClient(server.URL, "t"), org: "org"}

	// An exact name wins even though it is also a prefix of another project.
	project, err := s.findProject("Website")
	if err != nil || project.ID != "p1" {
		t.Fatalf("exact match = %+v, %v", project, err)
	}
	if project, err := s.findProject("internal"); err != nil || project.ID != "p3" {
		t.Errorf("case-insensitive match = %+v, %v", project, err)
	}
	if _, err := s.findProject("redes"); err != nil {
		t.Errorf("unique substring should match: %v", err)
	}
	// Ambiguity must fail loudly: picking one would bill the wrong project.
	if _, err := s.findProject("web"); err == nil || !strings.Contains(err.Error(), "matches several") {
		t.Errorf("ambiguous match should error, got %v", err)
	}
	if _, err := s.findProject("nothing"); err == nil {
		t.Error("unknown project should error")
	}
	// Archived projects are not startable.
	if _, err := s.findProject("Old Website"); err == nil {
		t.Error("archived project should not match")
	}
}

func TestStopEntrySendsOnlyEnd(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"data":{"id":"e1","start":"2026-07-15T13:00:00Z","end":"2026-07-15T14:00:00Z"}}`))
	}))
	defer server.Close()

	end := time.Date(2026, 7, 15, 14, 0, 0, 0, time.UTC)
	if _, err := newClient(server.URL, "t").stopEntry("org", "e1", end); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/v1/organizations/org/time-entries/e1" {
		t.Errorf("path = %q", gotPath)
	}
	if len(gotBody) != 1 || gotBody["end"] != "2026-07-15T14:00:00Z" {
		t.Errorf("body = %v, want only a UTC end", gotBody)
	}
}

func TestActiveEntryHandlesNull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":null}`))
	}))
	defer server.Close()

	entry, err := newClient(server.URL, "t").activeEntry()
	if err != nil {
		t.Fatal(err)
	}
	if entry != nil {
		t.Errorf("no running timer should decode to nil, got %+v", entry)
	}
}

func TestClientSurfacesAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"API resource not found"}`, http.StatusNotFound)
	}))
	defer server.Close()

	_, err := newClient(server.URL, "t").projects("org")
	if err == nil || !strings.Contains(err.Error(), "API resource not found") {
		t.Fatalf("error should quote the API message, got %v", err)
	}
}

func TestActiveEntryTreats404AsIdle(t *testing.T) {
	// Solidtime answers 404 when no timer runs. Propagating that as an error
	// used to break status, stop and toggle whenever nothing was tracking.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"No active time entry"}`, http.StatusNotFound)
	}))
	defer server.Close()

	entry, err := newClient(server.URL, "t").activeEntry()
	if err != nil {
		t.Fatalf("404 should mean idle, got error: %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil entry, got %+v", entry)
	}
}

func TestActiveEntryPropagatesRealErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Unauthenticated"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	if _, err := newClient(server.URL, "t").activeEntry(); err == nil {
		t.Fatal("a 401 must not be swallowed as idle")
	}
}

func TestInvoiceTotals(t *testing.T) {
	inv := Invoice{
		Lines: []Row{
			{Label: "Website", Seconds: 5400, Amount: 180},
			{Label: "Internal", Seconds: 1800, Amount: 60},
		},
	}
	if got := inv.totalHours(); got != 2.0 {
		t.Errorf("totalHours = %v, want 2", got)
	}
	if got := inv.totalAmount(); got != 240 {
		t.Errorf("totalAmount = %v, want 240", got)
	}
}

func TestRenderInvoiceFormats(t *testing.T) {
	inv := Invoice{
		Number:   "2026-07",
		Issued:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		Due:      time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		From:     time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:       time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		Currency: "€",
		Lines:    []Row{{Label: "Website", Seconds: 5400, Amount: 180}},
	}

	markdown, err := renderInvoice(inv, "markdown")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Invoice 2026-07", "1.50", "€180.00", "1 Jul 2026", "1 August 2026"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, markdown)
		}
	}

	csv, err := renderInvoice(inv, "csv")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csv, `"Total",1.50,180.00`) {
		t.Errorf("csv total wrong:\n%s", csv)
	}

	if _, err := renderInvoice(inv, "pdf"); err == nil {
		t.Error("an unknown format should error rather than emit nothing")
	}
}

func TestInvoiceHTMLEscapesLabels(t *testing.T) {
	// Project names are user data and end up in a file people open in a browser.
	inv := Invoice{
		Number:   `"><script>alert(1)</script>`,
		Currency: "€",
		Lines:    []Row{{Label: `A & B <img src=x onerror=alert(1)>`, Seconds: 3600, Amount: 10}},
	}
	html := renderInvoiceHTML(inv)

	if strings.Contains(html, "<script>") || strings.Contains(html, "<img") {
		t.Errorf("unescaped markup reached the output:\n%s", html)
	}
	if !strings.Contains(html, "A &amp; B") {
		t.Errorf("ampersand not escaped:\n%s", html)
	}
}

func TestResolveInvoiceRangeRequiresFlagsWhenScripted(t *testing.T) {
	flags := invoiceFlags{}
	if _, _, err := resolveInvoiceRange(&flags, false); err == nil {
		t.Fatal("a non-interactive run with no period must error, not prompt")
	}

	flags = invoiceFlags{period: "last-month"}
	start, end, err := resolveInvoiceRange(&flags, false)
	if err != nil {
		t.Fatal(err)
	}
	if start.Day() != 1 || end.Month() == time.Now().Month() {
		t.Errorf("last-month gave %s..%s", start.Format(time.DateOnly), end.Format(time.DateOnly))
	}

	flags = invoiceFlags{period: "nonsense"}
	if _, _, err := resolveInvoiceRange(&flags, false); err == nil {
		t.Error("an unknown period should error")
	}
}
