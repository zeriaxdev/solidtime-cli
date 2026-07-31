package main

// Aggregate is the payload of GET /v1/organizations/{org}/time-entries/aggregate.
//
// Note: this endpoint always returns null for Description and Color. The controller
// calls getAggregatedTimeEntries(), not the ...WithDescriptions() variant, so group
// keys are bare UUIDs that we resolve ourselves. See PLAN.md.
type Aggregate struct {
	Seconds     int     `json:"seconds"`
	Cost        *int    `json:"cost"`
	GroupedType string  `json:"grouped_type"`
	GroupedData []Group `json:"grouped_data"`
}

// Group is one row of an aggregation, possibly holding a second level of grouping.
type Group struct {
	Key         *string `json:"key"`
	Seconds     int     `json:"seconds"`
	Cost        *int    `json:"cost"`
	GroupedType string  `json:"grouped_type"`
	GroupedData []Group `json:"grouped_data"`
}

// Project is one entry of GET /v1/organizations/{org}/projects.
type Project struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Color      string   `json:"color"`
	ClientID   *string  `json:"client_id"`
	IsArchived bool     `json:"is_archived"`
	IsBillable bool     `json:"is_billable"`
	Rate       *float64 `json:"billable_rate"`
}

// Named covers the id/name-shaped collections: clients, tasks, tags, members.
type Named struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Organization is GET /v1/organizations/{org}. CurrencySymbol is derived
// server-side from Currency, so it is already the right glyph for the org.
type Organization struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Currency       string `json:"currency"`
	CurrencySymbol string `json:"currency_symbol"`
}

// TimeEntry is a single tracked interval. End is nil while the timer runs.
type TimeEntry struct {
	ID          string  `json:"id"`
	Start       string  `json:"start"`
	End         *string `json:"end"`
	Duration    *int    `json:"duration"`
	Description *string `json:"description"`
	ProjectID   *string `json:"project_id"`
	TaskID      *string `json:"task_id"`
	Billable    bool    `json:"billable"`
}

// User is GET /v1/users/me.
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Member is your row in an organization. Time entries belong to a member, not a user.
type Member struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

// Membership is one entry of GET /v1/users/me/memberships.
type Membership struct {
	Organization struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"organization"`
	Role string `json:"role"`
}
