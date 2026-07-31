package main

import (
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// Snapshot is everything a menu bar app or status line needs, in one process.
// Fetching these separately meant four process spawns and four rounds of auth.
type Snapshot struct {
	Status   StatusJSON        `json:"status"`
	Projects []SnapshotProject `json:"projects"`
	Today    SnapshotTotals    `json:"today"`
	Week     SnapshotTotals    `json:"week"`
	Currency string            `json:"currency"`
	Errors   []string          `json:"errors,omitempty"`
}

// SnapshotProject is the subset of a project a picker needs.
type SnapshotProject struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	IsBillable bool   `json:"is_billable"`
}

type SnapshotTotals struct {
	Hours  float64 `json:"hours"`
	Amount float64 `json:"amount"`
}

func newSnapshotCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "snapshot",
		Short: "Timer state, projects and today/week totals as one JSON document",
		Long: "Timer state, projects and today/week totals as one JSON document.\n\n" +
			"Built for menu bar apps and status lines: one process, one set of\n" +
			"credentials, and the API calls issued concurrently.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			s, err := newSession()
			if err != nil {
				return err
			}
			snapshot := collectSnapshot(s)

			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(snapshot)
		},
	}
}

// collectSnapshot runs the independent lookups concurrently. A failure in one
// section is recorded rather than thrown, so a broken total does not hide a
// perfectly good timer state.
func collectSnapshot(s *session) Snapshot {
	var (
		mutex    sync.Mutex
		wait     sync.WaitGroup
		snapshot Snapshot
	)

	fail := func(err error) {
		mutex.Lock()
		defer mutex.Unlock()
		snapshot.Errors = append(snapshot.Errors, err.Error())
	}

	projects := make(chan []Project, 1)

	wait.Add(1)
	go func() {
		defer wait.Done()
		found, err := s.client.projects(s.org)
		if err != nil {
			fail(err)
			projects <- nil
			return
		}
		projects <- found

		mutex.Lock()
		defer mutex.Unlock()
		for _, project := range found {
			if project.IsArchived {
				continue
			}
			snapshot.Projects = append(snapshot.Projects, SnapshotProject{
				ID:         project.ID,
				Name:       project.Name,
				Color:      project.Color,
				IsBillable: project.IsBillable,
			})
		}
	}()

	running := make(chan *TimeEntry, 1)

	wait.Add(1)
	go func() {
		defer wait.Done()
		entry, err := s.client.activeEntry()
		if err != nil {
			fail(err)
		}
		running <- entry
	}()

	now := time.Now()
	today := now.Format(time.DateOnly)
	weekStart := now.AddDate(0, 0, -((int(now.Weekday()) + 6) % 7)).Format(time.DateOnly)

	wait.Add(1)
	go func() {
		defer wait.Done()
		totals, err := periodTotals(s, today, today)
		if err != nil {
			fail(err)
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		snapshot.Today = totals
	}()

	wait.Add(1)
	go func() {
		defer wait.Done()
		totals, err := periodTotals(s, weekStart, today)
		if err != nil {
			fail(err)
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		snapshot.Week = totals
	}()

	wait.Add(1)
	go func() {
		defer wait.Done()
		org, err := s.client.organization(s.org)
		if err != nil {
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		snapshot.Currency = org.CurrencySymbol
	}()

	wait.Wait()

	// Naming the running entry's project needs both results, so it happens once
	// the concurrent phase is done.
	entry, found := <-running, <-projects
	snapshot.Status = buildStatus(entry, found)
	return snapshot
}

func periodTotals(s *session, from, to string) (SnapshotTotals, error) {
	params := url.Values{
		"start": {from + "T00:00:00Z"},
		"end":   {to + "T23:59:59Z"},
	}
	if s.cfg.Rounding.Minutes > 0 && s.cfg.Rounding.Type != "" {
		params.Set("rounding_type", s.cfg.Rounding.Type)
		params.Set("rounding_minutes", strconv.Itoa(s.cfg.Rounding.Minutes))
	}

	aggregate, err := s.client.aggregate(s.org, params)
	if err != nil {
		return SnapshotTotals{}, err
	}
	totals := SnapshotTotals{Hours: float64(aggregate.Seconds) / 3600}
	if aggregate.Cost != nil {
		totals.Amount = float64(*aggregate.Cost) / 100
	}
	return totals, nil
}

// buildStatus mirrors writeStatusJSON but takes an already-fetched project list.
func buildStatus(entry *TimeEntry, projects []Project) StatusJSON {
	if entry == nil {
		return StatusJSON{}
	}

	status := StatusJSON{
		Running:  true,
		ID:       entry.ID,
		Billable: entry.Billable,
		Start:    entry.Start,
		Seconds:  int(elapsed(*entry, time.Now()).Seconds()),
	}
	if entry.Description != nil {
		status.Description = *entry.Description
	}
	if entry.ProjectID != nil {
		status.ProjectID = *entry.ProjectID
		for _, project := range projects {
			if project.ID == *entry.ProjectID {
				status.Project, status.Color = project.Name, project.Color
			}
		}
	}
	return status
}
