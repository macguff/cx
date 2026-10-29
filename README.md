# cx

`cx` is a small Linux and macOS wrapper for running the official Codex CLI with isolated account profiles.

Each profile has its own `CODEX_HOME`. Authentication is performed by `codex login`; `cx` does not parse, display, refresh, or send credentials itself.

## Install

Download and verify the latest Linux or macOS release:

```sh
curl -fsSL https://raw.githubusercontent.com/macguff/cx/main/install.sh -o /tmp/cx-install.sh
sh /tmp/cx-install.sh
```

The default destination is `~/.local/bin/cx`. Set `CX_INSTALL_DIR` to install
elsewhere, or `CX_VERSION` to install a specific release such as `v1.0.0`.

## Build

Requires Go 1.22 or newer and an installed `codex` command on `PATH`.

Build and install on Linux or macOS:

```sh
cd /path/to/cx
go build -o cx ./cmd/cx
install -m 0755 cx ~/.local/bin/cx
```

Make sure `~/.local/bin` is on your `PATH`, then verify the installation:

```sh
cx --version
cx help
```

After pulling changes, run the same build and install commands again to replace
the installed binary. To build without installing, use `go build -o cx ./cmd/cx`.

## Usage

Create profiles using the official login flow:

```sh
cx login personal
cx login work --device-auth
```

Select and launch the default profile:

```sh
cx use work
cx
```

Quickly switch the default profile:

```sh
cx switch
```

With exactly two profiles, this immediately selects the other one. With three
or more profiles, it opens a numbered terminal selector. Switching only affects
future `cx` commands; Codex processes that are already running keep their
original profile.

Run a different profile without changing the default:

```sh
cx run personal
cx run work exec "review this repository"
cx run work resume
```

Resume a saved session with the current profile:

```sh
cx resume
```

Use `--last` when you want to select the newest session without prompting:

```sh
cx resume --last
```

Inspect profiles:

```sh
cx list
cx current
cx path work
```

Open the interactive account selector:

```sh
cx tui
```

The TUI lists every profile with its local token usage. Select a profile number
to make it current and launch the official Codex CLI. Use `n` to create a profile
through the official `codex login` flow, `r` to switch between 7-day, 30-day, and
all-time totals, `w` to manage wake-up schedules, or `q` to exit. Account selection is always manual; `cx` never
rotates accounts based on usage.

Usage totals are read locally from each profile's `sessions/**/rollout-*.jsonl`.
Cached input is already part of input, and reasoning output is already part of
output, so neither is counted twice. Copied fork history and repeated cumulative
usage events are deduplicated. The totals are local-log estimates: missing or
deleted rollout events cannot be reconstructed and may not match server billing.

Show the local usage summary for every profile:

```sh
cx status
```

This reports the last 5 hours, last 7 days, and all-time totals. Authentication
is checked with the official `codex login status` command for each profile; `cx`
does not parse credential contents. For server-side remaining limits and reset
times, run `cx wake web` and refresh actual quota through the official Codex
app-server interface. You can also start the selected profile and enter `/status`
inside Codex, or use the official usage dashboard. The profile
selected with `cx use <name>` is marked `(current)`.

Show the same local history grouped by model:

```sh
cx usage
```

This lists every profile and model with totals for the last 5 hours, last 7
days, and all time. It also shows monthly totals, month-over-month changes, and
a terminal sparkline from the first recorded month through the current month.
It lists the five highest distinct rolling 5-hour windows with their local dates,
and the highest 5-hour window ending in each Monday-to-Sunday week. Estimated USD
values use each model's [standard API text-token prices](https://developers.openai.com/api/docs/pricing)
for uncached input, cached input, and output; the current price table is applied
to all history. These values are a way to compare usage, not actual Codex charges
or percentages of a subscription limit. The logs do not expose the server's
5-hour quota. Older events without model metadata are grouped as `unknown` and
omitted from cost estimates with a notice.

To include usage history from an existing default Codex installation, first
create the destination profile, then import only its local rollout logs:

```sh
cx login personal
cx import personal
```

This copies `~/.codex/sessions/**/rollout-*.jsonl` only. Credentials,
configuration, and other files from `~/.codex` are never copied.

Log out one profile without affecting the others:

```sh
cx logout work
```

## Scheduled wake-ups

Run `cx wake` (or choose `w` in `cx tui`) for the Bubble Tea terminal manager.
Use Tab or left/right to switch between plans, accounts, history and background
settings; up/down selects a row. Press `n` to create a plan, `e` to edit, `p`
to pause/resume, `w` for a confirmed manual request, or `f` on an account to
refresh its quota. The form uses Tab to move and Ctrl+S to save. Redirected
input retains the numbered line menu.

`cx wake install` and `cx wake serve` run scheduling **without Web by default**.
Run `cx wake web` to explicitly enable Web and obtain its private access link;
`cx wake web off` disables it again. This choice is saved in the wake data
store. `CX_WAKE_NO_WEB=1` can force scheduling without a listener.

Open the built-in local dashboard:

```sh
cx wake web
```

This starts the background scheduler and prints an access link. After updating
the cx executable, running the command again automatically replaces an older
managed background process, preserving tasks and history. If a request is in
progress, wait for it to finish and run the command again.
Open the complete link in your browser. The UI includes:

- Profile cards with actual remaining quota, window length, reset countdown,
  and the timestamp of the last query. Use **刷新实际额度** to call the official
  `account/rateLimits/read` RPC; this does not start a model turn. Failed or old
  snapshots remain visibly marked, and unavailable limits are not inferred from
  token totals. Accounts may have windows other than five hours.
- A local 5-hour sliding activity chart and a 24-hour timeline comparing actual
  server windows with scheduled requests and their nominal `+5h` references.
- Forms for creating, editing, pausing, resuming, and deleting plans, with
  presets, a same-day timeline for daily plans, and upcoming execution dates.
  New Web plans default
  to `gpt-6.1-terra` with `low` reasoning, and both settings can be changed.
  One-time, daily, weekday,
  and interval schedules support explicit timezones and optional models.
- Manual wake-up controls, account/result filters, and execution history.
  A manual wake-up requires confirmation because it sends a real model request.

The UI listens on `127.0.0.1:7331` by default, uses another free local port if
that port is occupied, and stores its access capability in the private
`<cx data directory>/wake/web.json` file. `cx wake web` always prints the current
access link; keep it private. The capability is passed in the URL fragment and
then used for authenticated local API requests. To select a port explicitly, set
`CX_WAKE_WEB_ADDR=127.0.0.1:7331` when starting/installing the background. Remote
machines can be reached with SSH forwarding to the same local port; the UI is
not exposed on public interfaces.

Use `cx wake`, or press `w` in `cx tui`, for the numbered terminal manager.
Choose a profile, schedule, timezone, and optional model, then review the next
three request times before saving. Select a task to run it now, pause/resume it,
or delete it. The menu also shows execution history and background controls.

```sh
cx wake
cx wake add work --weekdays 07:00 --tz Asia/Shanghai
cx wake add work --daily 07:00,12:01,17:02 --tz Asia/Shanghai
cx wake add personal --after 2h
cx wake add personal --at '2026-11-01 07:00' --tz Asia/Shanghai
cx wake add work --every 5h1m --model <model-id>
cx wake list
cx wake history
```

Each request consumes real usage. The preview's `+5h reference` is a planning
aid; a successful request does not establish that a new quota window started.
Existing windows, remaining limits, and reset times are controlled by OpenAI.
Check `/status` in Codex or the official usage dashboard for actual limits.

Enable background checks after building/installing `cx` at a stable path:

```sh
cx wake install
cx wake status
```

On Linux, cx checks that the user systemd manager is reachable before installing
a persistent service. If it is missing or reports `Failed to connect to bus`, cx automatically
starts an independent background process. It continues after the terminal
closes, but **does not restart automatically after reboot**; run
`cx wake install` again to restart it. Repeating install while it is
running reuses the existing process. `status` and `uninstall` manage the chosen
backend. macOS uses a LaunchAgent.

The scheduler and Web UI run together; checks run every 15 seconds while the
computer is awake. Earlier systemd timers are replaced by the persistent service
on installation. `cx wake web` upgrades an older managed backend when the
executable changes; a foreground `cx wake serve` must be stopped in its own
terminal. Changing the Codex installation/PATH without updating cx requires
reinstalling the service. For
systemd, logs are available through `journalctl --user -u cx-wake.service`; the
process backend and macOS write `<cx data directory>/wake/scheduler.log`.

For a foreground terminal or your own scheduler:

```sh
cx wake serve                   # keep running; Ctrl-C stops checks
cx wake tick                    # check due tasks once, suitable for cron
```

Recurring schedules stay anchored after manual runs and delays. Offline backlog
is coalesced into the latest due slot; if that slot is over 5 minutes late, it is
recorded as missed without sending a request. Requests are serialized and time
out after 90 seconds. Failed or interrupted attempts are recorded without
automatic retries. Pausing/removing a task stops future executions; an existing
request can finish. Uninstalling the service keeps tasks and history:

```sh
cx wake pause <id>
cx wake resume <id>
cx wake run <id>                # explicit request; leaves the schedule in place
cx wake remove <id>
cx wake uninstall
cx wake help
```

Wake-ups use the profile's saved authentication through official `codex exec`.
The CLI must support `--ignore-user-config` and `--ephemeral`. A fixed `OK` prompt
runs from an empty temporary directory, with a read-only sandbox and no
interactive approvals. The profile's config and project instructions are
excluded. Tasks created without a model use the CLI default; set `--model` in
the terminal or choose a model and reasoning level in the Web form.
Inherited API-key variables are removed. Saved API-key authentication still uses
API billing. Ephemeral requests do not appear in cx's rollout-based usage totals.

Tasks and the latest 100 execution records are saved under
`<cx data directory>/wake/state.json` with mode `0600`. See
[`docs/wake.md`](docs/wake.md) for scheduling behavior and design references.

## Storage

On Linux and macOS, the default paths are:

- config: `${XDG_CONFIG_HOME:-$HOME/.config}/cx/config.json`
- profiles: `${XDG_DATA_HOME:-$HOME/.local/share}/cx/profiles/<name>`

Every profile directory is created with mode `0700`. Its `config.toml` sets:

```toml
cli_auth_credentials_store = "file"
```

This keeps the profile's credentials in that profile's `CODEX_HOME/auth.json` instead of sharing an operating-system keyring entry. Treat every profile directory like a password: it contains access tokens and must not be committed or shared.

`cx` never changes `~/.codex` and never uses symlinks. A running Codex process therefore keeps the same profile directory for its entire lifetime.

## Development

GitHub Actions runs tests on Linux and macOS for every push and pull request.
Version tags build and publish release binaries automatically. See
[`docs/release.md`](docs/release.md) for the release process.
