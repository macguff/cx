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
all-time totals, or `q` to exit. Account selection is always manual; `cx` never
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
does not parse credential contents. Server-side remaining limits and reset times
are not exposed by a scriptable public command. Start the selected profile and
enter `/status` inside Codex, or use the official usage dashboard. The profile
selected with `cx use <name>` is marked `(current)`.

Show the same local history grouped by model:

```sh
cx usage
```

This lists every profile and model with totals for the last 5 hours, last 7
days, and all time. It also shows monthly totals, month-over-month changes, and
a terminal sparkline from the first recorded month through the current month.
Older events without model metadata are grouped as `unknown`.

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
