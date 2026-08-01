# solidtime-cli

[![standard-readme compliant](https://img.shields.io/badge/readme%20style-standard-brightgreen.svg?style=flat-square)](https://github.com/RichardLitt/standard-readme)

> Report tracked hours and billable totals from solidtime, from your terminal.

`solidtime-cli` answers the questions you would otherwise open the [solidtime](https://www.solidtime.io)
web app for: what did I work on today, how many hours did that client get last month, and what do I
invoice for it. It reads the solidtime v1 API, renders a table for humans and TSV, CSV, JSON or
Markdown for everything else, and never writes to your time entries.

```
$ solidtime --last-month
2026-06-01 .. 2026-06-30

● Website     12.00 h   €1200.00
● Internal     3.50 h    €350.00

              15.50 h   €1550.00
```

## Table of Contents

- [Background](#background)
- [Install](#install)
- [Usage](#usage)
  - [Invoices](#invoices)
  - [The timer](#the-timer)
  - [Selecting a period](#selecting-a-period)
  - [Grouping](#grouping)
  - [Output formats](#output-formats)
  - [Money](#money)
  - [Configuration](#configuration)
- [API notes](#api-notes)
- [Maintainers](#maintainers)
- [Contributing](#contributing)
- [License](#license)

## Background

Solidtime's API is capable but its published `openapi.json` is stale, which makes the first hour of
integration unnecessarily expensive. Two findings cost the most time and are worth stating up front:

- Routes are `/v1/organizations/{org}/...`, **plural**. The committed spec says `organization` and
  documents four endpoints out of dozens; `routes/api.php` is the real contract.
- `/time-entries/aggregate` always returns `null` for `description` and `color`, because the
  controller calls `getAggregatedTimeEntries()` rather than the `...WithDescriptions()` variant. Group
  keys come back as bare UUIDs and every client has to resolve them itself.

This tool encodes both, so you do not have to find out the same way.

## Install

```sh
brew install zeriaxdev/tap/solidtime
```

Or with Go:

```sh
go install github.com/zeriaxdev/solidtime-cli@latest
```

From a clone:

```sh
git clone https://github.com/zeriaxdev/solidtime-cli.git
cd solidtime-cli
go build -o ~/.local/bin/solidtime .
```

Requires Go 1.24 or newer. Then set up credentials:

```sh
solidtime config init      # writes ~/.config/solidtime/config.toml, mode 0600
```

Put your API token in that file. The web app issues one under **Profile → API Tokens**. Leave
`default_org` empty if you belong to exactly one organization; otherwise `solidtime orgs` lists the
ids.

## Usage

```sh
solidtime                                   # today, grouped by project
solidtime --last-month --billable
solidtime --this-week --group day
solidtime --from 2026-05-01 --to 2026-05-31 --project website

solidtime start "fixing the invoice bug" -p website
solidtime status --short
solidtime stop

solidtime projects                          # names, colors, billable rates
solidtime orgs                              # organization ids
solidtime completion zsh                    # shell completion
```

### Invoices

```sh
solidtime invoice                                  # guided picker, writes invoice-2026-07.xlsx
solidtime invoice --period last-month --format pdf
solidtime invoice --from 2026-07-01 --to 2026-07-31 --client <id> --format html
```

Run bare in a terminal and it walks you through period, grouping and client with an arrow-key
picker. Pass any of `--period`, `--from`/`--to`, `--client` or `--project` and it skips the picker
entirely, so it scripts cleanly and works in CI.

A file is always written — `invoice-<number>.<ext>` in the current directory unless you pass
`--output`. Use `-o -` to send it to stdout instead.

| `--format` | Rendered by | Notes |
| --- | --- | --- |
| `xlsx` | solidtime | The default. The same spreadsheet the web app exports. |
| `ods` | solidtime | For LibreOffice. |
| `pdf` | solidtime | **Paid plans only** — free plans get "Feature is not available in free plan". |
| `html` | locally | Print-styled: open in a browser and print to PDF. The free-plan route to a PDF. |
| `markdown` | locally | For pasting into an email or an issue. |
| `csv` | locally | Plain rows, no chart. |

`pdf`, `xlsx` and `ods` go through solidtime's own `/time-entries/aggregate/export` endpoint, so the
file is byte-for-byte what the web app produces, chart included. The local formats are built from
the aggregate data and the rates in [Money](#money).

**Solidtime has no invoicing API** — the `invoices:*` permissions exist in its source but no routes
back them. What this command produces is a time report shaped like an invoice, not a record stored
in solidtime.

Line items come from `--group` (`project`, `client`, `task` or `day`), and only billable time is
included unless you pass `--billable=false`.

### The timer

```sh
solidtime start [description] [-p PROJECT] [-b] [--force]
solidtime stop
solidtime toggle [description] [-p PROJECT]
solidtime status [--short]
```

`start` refuses to run when a timer is already going; `--force` stops the old one first.
`-p` matches a project by name or a unique part of it, and an ambiguous match is an error rather
than a guess. Starting on a project that is marked billable sets `billable` without needing `-b`.

`toggle` stops the timer if one is running and starts it otherwise — the single-command form for a
hotkey or a menu bar button.

`status --short` prints one line, `1:05 fixing the invoice bug`, and prints **nothing** when no
timer runs, so a status line goes empty rather than stale:

```sh
solidtime status --short    # 1:05 fixing the invoice bug
```

### Selecting a period

Bare `solidtime` reports today. Everything longer is an explicit flag: `--yesterday`, `--this-week`,
`--last-week`, `--this-month`, `--last-month`, or `--from`/`--to` for an exact range. Weeks start on
Monday. Ranges are inclusive of both end dates.

### Grouping

`--group` accepts `project`, `client`, `task`, `user`, `tag`, `description`, `day`, `week`, `month`,
`year` and `billable`. Add `--sub-group` for a second level, rendered indented beneath the first:

```sh
solidtime --last-month --group client --sub-group project
```

`--project SUBSTR` filters rows by name, case-insensitively, after grouping.

### Output formats

| `--format` | For |
| --- | --- |
| `table` | Humans. Aligned, colored per project, this is the default. |
| `plain` | Scripts. Tab-separated `label`, `hours`, `amount`. No color, no symbol, no header, no alignment. |
| `csv` | Spreadsheets. Includes a `group` column for sub-grouped reports. |
| `json` | Piping to `jq`. Carries per-row and grand totals. |
| `markdown` | Pasting into an invoice, an issue, or a client email. |

`--total` drops the per-group rows and prints only the summed line, in whichever format you asked
for. Combined with `plain` it gives you one bare line to cut up:

```sh
$ solidtime --last-month --total --format plain
TOTAL	15.50	1550.00

$ solidtime --this-week --total --format plain | cut -f2
15.50
```

Color turns itself off when stdout is not a terminal, when `NO_COLOR` is set, or with `--no-color`.

### Money

The amount column resolves in this order:

1. `--rate` on the command line,
2. a per-project rate under `[rates]` in the config, keyed by lowercased project name, with
   `default` covering the rest,
3. solidtime's own billable cost.

Solidtime returns no cost at all when you hold the Employee role and your organization has
`employees_can_see_billable_rates` disabled — configure `[rates]` in that case. The currency symbol
comes from your organization's setting; `currency` in the config overrides it. Nothing is ever
converted between currencies.

`--round 15` rounds each entry to the nearest 15 minutes before totaling. This is a solidtime
premium feature applied server-side, and is ignored silently on free plans.

### Configuration

`~/.config/solidtime/config.toml`, created by `solidtime config init` with mode `0600`. The file is
refused if it is group- or world-readable.

```toml
token       = "..."
default_org = "..."        # optional, auto-detected when you have one organization
# api_url   = "..."        # optional, for self-hosted instances
# currency  = "€"          # optional, overrides your organization's symbol

[rates]                    # optional, used when solidtime has no rate configured
default = 95.0
website = 120.0

[rounding]                 # optional, premium-gated server-side
type    = "nearest"
minutes = 15
```

Environment variables override the file: `SOLIDTIME_API_TOKEN`, `SOLIDTIME_ORGANIZATION_ID`,
`SOLIDTIME_API_URL`.

## API notes

`PLAN.md` records the verified behavior of the endpoints this tool uses — parameter names, response
shapes, the fact that `cost` is integer cents and `null` for restricted roles — along with the
remaining milestones. Read it before changing `client.go`.

## Maintainers

[@zeriaxdev](https://github.com/zeriaxdev)

## Contributing

Issues and PRs welcome. Run `go test ./...` before opening one; the suite covers date-range math,
money resolution, name resolution and the HTTP layer against a fake solidtime, which is where a
wrong invoice would come from.

## License

[MIT](LICENSE) © zeriaxdev
