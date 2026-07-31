# solidtime-cli

A Go CLI for solidtime: report tracked hours and billable totals, and drive the running timer,
without opening the web app.

Binary name: `solidtime`.

## Why Go

Single static binary, brew-tappable, no runtime dependency. The existing Python script at
`~/.local/bin/solidtime-hours` is the reference implementation and stays until M1 reaches parity,
then gets deleted.

## API notes (verified against solidtime-io/solidtime@main)

These cost real time to rediscover, so they are written down.

- Base URL: `https://app.solidtime.io/api`. Auth: `Authorization: Bearer <token>` (JWT).
- **Routes use `/v1/organizations/{org}/...` — plural.** The published `openapi.json` in the repo
  root says `organization` (singular) and only documents 4 endpoints. It is stale. `routes/api.php`
  is the source of truth.
- **`/time-entries/aggregate` never returns names or colors.** The controller calls
  `getAggregatedTimeEntries()`, not `getAggregatedTimeEntriesWithDescriptions()`, so `description`
  and `color` are always `null`. Group keys are UUIDs. The web app resolves them client-side from
  its own project store, and so must we: fetch `/projects`, `/clients`, `/tasks`, `/tags` once and
  build an id → {name, color} map.
- `aggregate` params: `group`, `sub_group` (both from `day|week|month|year|user|project|task|client|
  billable|description|tag|type`), `start`/`end` (strict `Y-m-d\TH:i:s\Z`), `billable=true|false`,
  `project_ids[]`, `client_ids[]`, `tag_ids[]`, `task_ids[]`, `member_id`, `rounding_type`,
  `rounding_minutes`, `fill_gaps_in_time_groups`.
- `aggregate` response: `{data: {seconds, cost, grouped_type, grouped_data: [{key, seconds, cost,
  grouped_type, grouped_data}]}}`. `cost` is **integer cents** in the org currency, and is `null`
  when the caller may not see billable rates (Employee role + `employees_can_see_billable_rates`
  off).
- Org id discovery: `GET /v1/users/me/memberships`.
- Rounding is premium-gated server-side; the params are silently ignored otherwise.

## Layout

Flat, single `package main`. Split into packages when a file crosses ~300 lines, not before.

```
~/src/solidtime-cli/
  main.go        cobra root, global flags, config load
  config.go      TOML config + env override + resolve order
  client.go      HTTP client, auth, error unwrapping
  types.go       API response structs
  report.go      report command: aggregate + name resolution
  timer.go       start / stop / status
  render.go      table (text/tabwriter), ANSI color, --json, --csv
  main_test.go   table tests + httptest fake server
```

### Dependencies

- `spf13/cobra` — subcommands, flags, shell completion. Justified by 6+ commands; stdlib `flag`
  gets unpleasant past two.
- `BurntSushi/toml` — Go has no stdlib TOML. If this feels like too much for one config file,
  switch to JSON with `encoding/json` and drop the dep. TOML wins only because the file is
  hand-edited and takes comments.

Everything else is stdlib: `net/http`, `text/tabwriter`, `encoding/csv`, `time`.
Colors are raw ANSI truecolor escapes from the project's hex — no lipgloss.

## Config

`~/.config/solidtime/config.toml`, created by `solidtime config init` with mode `0600`.

```toml
default_org = "00000000-0000-0000-0000-000000000000"
api_url     = "https://app.solidtime.io/api"   # optional, for self-hosted
token       = "..."
currency    = "EUR"                             # display only

[rates]                       # optional flat-rate overrides when solidtime has no rate set
default = 95.0
website = 120.0

[rounding]                    # optional, premium-gated server-side
type    = "nearest"
minutes = 15
```

Resolution order, highest first: command-line flag → env var (`SOLIDTIME_API_TOKEN`,
`SOLIDTIME_ORGANIZATION_ID`, `SOLIDTIME_API_URL`) → config file → default. Env stays supported so
CI and one-off shells keep working.

Config file is refused if it is group/world-readable, with a message telling you to chmod it.

## Commands

```
solidtime report [flags]              # default command when none given
  --from / --to YYYY-MM-DD
  --last-month | --this-week | --last-week | --today
  --group project|client|task|user|tag|description|day|week|month
  --sub-group <same set>              # two-level nesting, indented output
  --project SUBSTR                    # client-side filter on resolved name
  --billable                          # billable time only
  --rate FLOAT                        # flat hourly override, beats config rates
  --round 15                          # nearest-N-minute rounding
  --json | --csv                      # machine-readable; --csv uses the server export endpoint
  --no-color

solidtime start [description] [--project NAME] [--task NAME] [--billable]
solidtime stop
solidtime status [--short]            # --short is one line for a prompt/statusline
solidtime projects [--archived]       # id, colored dot, name, client, rate
solidtime share [report flags]        # POST /reports, prints the public URL
solidtime orgs                        # list memberships + ids
solidtime config init | config path
solidtime completion bash|zsh|fish
```

Default with no args is `report` over the current month, matching today's behavior.

## Milestones

Each is a small, independently shippable commit series. Conventional commits.

- **M0 — scaffold.** `go mod init`, cobra root, config load + `config init`, `client.go` with auth
  and error unwrapping, `solidtime orgs`. Done when `solidtime orgs` prints your membership.
- **M1 — report parity + the gap.** `aggregate` call, name resolution from `/projects`, colored
  dots, tabwriter table, billable cost from cents, `--from/--to/--last-month/--billable/--rate`.
  Done when output matches `solidtime-hours` but shows real project names. **Delete the Python
  script here.**
- **M2 — grouping and machine output.** `--group`, `--sub-group` with indented nesting, `--json`,
  `--csv` via the server export endpoint.
- **M3 — invoice shape.** Config `[rates]` per-project overrides, `[rounding]`, currency symbol,
  subtotals per group, markdown table output.
- **M4 — timer.** `start`, `stop`, `status`, `--short`. Needs `POST /time-entries` and
  `/users/me/time-entries/active`.
- **M5 — distribution.** Shell completions, `goreleaser`, homebrew tap, README.

## Testing

One `main_test.go`. Table tests for the logic that can silently produce a wrong invoice:

- date range math (`--last-month` across a year boundary, month lengths, DST-free UTC formatting)
- cents → currency, flat-rate override precedence, `cost: null` handling
- name resolution including unknown/`null` keys → `(no project)`
- filtered totals recomputed from rows, never taken from the unfiltered API total
- config resolution order (flag > env > file)

Plus an `httptest.Server` returning one recorded `aggregate` payload, so the client, resolution and
render path run end to end. No mocking framework, no fixtures directory beyond one JSON file.

## Deliberately out of scope

Member management, invitations, importers, project/client CRUD. That is web-app work. Add a command
only after wanting it twice.
