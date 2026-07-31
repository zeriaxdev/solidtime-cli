package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client talks to a solidtime instance. Routes are /v1/organizations/... (plural);
// the openapi.json published in the solidtime repo is stale on this point.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func newClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// get unmarshals the "data" envelope every v1 endpoint wraps its payload in.
func (c *Client) get(path string, params url.Values, out any) error {
	endpoint := c.baseURL + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return httpError{status: res.StatusCode, endpoint: endpoint, message: apiError(body)}
	}

	envelope := struct {
		Data json.RawMessage `json:"data"`
	}{}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decoding %s: %w", endpoint, err)
	}
	return json.Unmarshal(envelope.Data, out)
}

// send performs a write request, unwrapping the same "data" envelope as get.
// out may be nil when the response body is not needed.
func (c *Client) send(method, path string, body, out any) error {
	endpoint := c.baseURL + path

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	responseBody, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("%s on %s: %s", res.Status, endpoint, apiError(responseBody))
	}
	if out == nil {
		return nil
	}

	envelope := struct {
		Data json.RawMessage `json:"data"`
	}{}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return fmt.Errorf("decoding %s: %w", endpoint, err)
	}
	return json.Unmarshal(envelope.Data, out)
}

// httpError carries the status code so callers can treat specific failures as
// data rather than errors.
type httpError struct {
	status   int
	endpoint string
	message  string
}

func (e httpError) Error() string {
	return fmt.Sprintf("%d on %s: %s", e.status, e.endpoint, e.message)
}

// isNotFound reports whether an error was a 404 from the API.
func isNotFound(err error) bool {
	var apiErr httpError
	return errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound
}

// apiError pulls Laravel's "message" out of an error body, falling back to the raw body.
func apiError(body []byte) string {
	parsed := struct {
		Message string `json:"message"`
	}{}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Message != "" {
		return parsed.Message
	}
	return string(body)
}

func (c *Client) memberships() ([]Membership, error) {
	var out []Membership
	err := c.get("/v1/users/me/memberships", nil, &out)
	return out, err
}

func (c *Client) organization(orgID string) (Organization, error) {
	var out Organization
	err := c.get("/v1/organizations/"+orgID, nil, &out)
	return out, err
}

func (c *Client) projects(orgID string) ([]Project, error) {
	var out []Project
	err := c.get("/v1/organizations/"+orgID+"/projects", url.Values{"limit": {"500"}}, &out)
	return out, err
}

// named fetches an id/name collection: clients, tasks or tags.
func (c *Client) named(orgID, collection string) ([]Named, error) {
	var out []Named
	err := c.get("/v1/organizations/"+orgID+"/"+collection, url.Values{"limit": {"500"}}, &out)
	return out, err
}

func (c *Client) aggregate(orgID string, params url.Values) (Aggregate, error) {
	var out Aggregate
	err := c.get("/v1/organizations/"+orgID+"/time-entries/aggregate", params, &out)
	return out, err
}

func (c *Client) me() (User, error) {
	var out User
	err := c.get("/v1/users/me", nil, &out)
	return out, err
}

// memberID finds your Member row in an organization. Creating a time entry needs
// the member id, which is per-organization and not the same as your user id.
func (c *Client) memberID(orgID, userID string) (string, error) {
	var members []Member
	if err := c.get("/v1/organizations/"+orgID+"/members", url.Values{"limit": {"500"}}, &members); err != nil {
		return "", err
	}
	for _, member := range members {
		if member.UserID == userID {
			return member.ID, nil
		}
	}
	return "", fmt.Errorf("you do not appear to be a member of organization %s", orgID)
}

// activeEntry returns the running timer, or nil when nothing is running.
//
// Solidtime answers 404 rather than a null payload when no timer is running, so
// that case has to be read as "idle" instead of propagated as a failure.
func (c *Client) activeEntry() (*TimeEntry, error) {
	var out *TimeEntry
	if err := c.get("/v1/users/me/time-entries/active", nil, &out); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

func (c *Client) startEntry(orgID string, entry map[string]any) (TimeEntry, error) {
	var out TimeEntry
	err := c.send(http.MethodPost, "/v1/organizations/"+orgID+"/time-entries", entry, &out)
	return out, err
}

// stopEntry sets an end time. The update endpoint accepts a partial body, so
// nothing else about the entry has to be resent.
func (c *Client) stopEntry(orgID, entryID string, end time.Time) (TimeEntry, error) {
	var out TimeEntry
	body := map[string]any{"end": end.UTC().Format(apiTimeFormat)}
	err := c.send(http.MethodPut, "/v1/organizations/"+orgID+"/time-entries/"+entryID, body, &out)
	return out, err
}

// resolveOrg returns the configured org, or the only membership if there is exactly one.
func resolveOrg(client *Client, configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	memberships, err := client.memberships()
	if err != nil {
		return "", err
	}
	if len(memberships) == 1 {
		orgID := memberships[0].Organization.ID
		rememberOrg(orgID)
		return orgID, nil
	}
	return "", fmt.Errorf("set default_org in your config (or SOLIDTIME_ORGANIZATION_ID); run 'solidtime orgs' to list them")
}
