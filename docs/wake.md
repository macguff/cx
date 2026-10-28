# Wake-up scheduling

The wake-up manager takes its task/schedule/history interaction from
[cockpit-tools](https://github.com/jlcodes99/cockpit-tools). cx keeps its existing
profile isolation and delegates authentication and inference to the official
CLI. No credential parser or direct quota/inference HTTP client is added.

## User flow

`cx wake web` starts or upgrades the background and prints the local dashboard
access link. Assets are embedded in the Go binary; no Node runtime, separate
frontend deployment, or external asset service is needed. The browser manages
the same state file as the CLI, including editing an existing task without
changing its ID. Editing an interval re-anchors it to the save time. Editing a
running scheduled task is rejected until its current attempt finishes.

The health endpoint identifies the running executable by its SHA-256 digest,
captured at server startup. When `cx wake web` finds a different or older build,
it verifies that the configured service owns the listener and restarts it with
the current executable. Concurrent lifecycle commands are serialized. An
active request is never interrupted for an upgrade; retry after it finishes.
An independently started foreground server must be stopped in its terminal.

Actual quota is fetched only when the user requests a refresh, through the
official `codex app-server` stdio handshake and `account/rateLimits/read` RPC.
No thread or turn is created, and cx still does not parse authentication data.
The response's actual bucket IDs, durations, percentages, and reset timestamps
are cached in private `quotas.json`; failed refreshes preserve the last known
snapshot alongside the error and attempt time. The UI labels stale snapshots
and keeps local 5-hour token activity separate from quota percentages.

`cx wake` and the `w` action in `cx tui` share the same numbered terminal menu:

1. Add a task and choose its profile.
2. Choose once, daily, weekdays, or a fixed interval. Daily and weekday tasks
   accept multiple `HH:MM` times.
3. Choose an IANA timezone (default: the computer's local timezone) and optional
   model ID.
4. Preview three future requests and their nominal `+5h` planning references.
5. Save, then enable the background scheduler through the menu or
   `cx wake install`.

Each task targets one explicit profile. Creating or executing tasks leaves the
default profile unchanged. Use the Web dashboard to edit a schedule or model,
or remove and recreate the task in the terminal. A manual run is an additional
request and preserves the plan.

## Execution and recovery

The persistent systemd service, LaunchAgent, detached process, foreground worker,
and external cron jobs share the same execution logic. `state.lock` protects short state transactions;
`execution.lock` serializes model requests across processes using the same data
directory. An active request does not block listing, pausing, or removing tasks.

Before starting Codex, cx records `running` and advances the task's next run.
This favors avoiding duplicate requests: an executor crash may lose an attempt,
but does not replay a request whose server outcome is unknown. A later executor
marks unfinished history as `interrupted`. Histories survive task deletion.

After downtime, older slots are coalesced into the most recent due slot. cx runs
at most one request per task per check, and only within a five-minute grace
period. Otherwise it records `missed` and advances to the next future slot.
Intervals remain anchored to task creation; daily and weekday tasks follow their
chosen civil timezone. Nonexistent daylight-saving times are skipped; repeated
wall times are scheduled once per calendar date. `Local` follows the computer's
timezone, so use an explicit timezone to keep a plan stable while travelling.

The CLI runs with a 90-second deadline in a temporary empty workspace, using
`CODEX_HOME` for the selected profile. It receives the fixed instruction to reply
`OK`, with `--ignore-user-config`, `--ephemeral`, `--json`, a read-only sandbox,
`approval_policy="never"`, `cli_auth_credentials_store="file"`, and
`project_doc_max_bytes=0`. cx requires a `turn.completed` event and successful CLI
exit before recording success. Raw response text, stderr, and credentials are
not saved in task history. Cancelling a request terminates its process group.

Services save the cx executable path and PATH/XDG/timezone environment needed to
locate the same profile store and Codex installation. They contain no credentials
and require reinstalling after either executable moves. Installing a service
does not prevent sleep or configure wake-from-sleep behavior.

The systemd and launchd backends run `wake serve` continuously so the Web UI is
available between scheduled requests. Installation disables/removes a legacy
systemd timer before enabling the persistent service. The process backend
recognizes an older worker without Web UI support, stops it through the existing
control protocol, and starts the new binary with the same saved task store.

On Linux, finding the `systemctl` binary is insufficient: cx probes the actual
user manager with `systemctl --user show --property=Version` before writing unit
files. If no user manager is reachable, install starts cx in a new process
session with stdin closed and output redirected to `wake/scheduler.log`.
This backend needs no systemd, cron daemon, or D-Bus session. It does not register
an automatic login/boot start; explicitly run install again after reboot or an
unexpected exit. Status distinguishes a configured but stopped process from a
running one.

`service.lock` serializes lifecycle commands, and the detached process holds
`worker.lock` for its lifetime. Startup is acknowledged through atomic
`worker.json` metadata before install reports success. Uninstall sends a control
file scoped to the current worker's random token, waits for its lifetime lock to
be released, and then removes process metadata. It never signals a saved PID,
which may have been reused by an unrelated process. Stale stop requests do not
affect a restarted worker. Stopping a worker cancels an in-flight Codex request;
saved tasks and history remain. If a previous failed installation left systemd
unit files, the process backend leaves those files in place and reports them
when uninstalled; they can be removed with uninstall once the user manager is
available again.

## Local web boundary

The HTTP listener only accepts a loopback IP. All data and mutation API routes
require a random 256-bit bearer capability generated for that server lifetime.
The CLI prints the capability in a URL fragment, which is removed from browser
history after it is copied into session storage. Static assets do not contain
profile data. API requests reject foreign Host/Origin headers and cross-site
fetches; the page loads no third-party scripts, styles, or fonts. Request sizes
are capped, profile/task names are validated, and response/error paths never
serve credential files or raw Codex output.

`web.lock` prevents multiple listeners for one data directory; private
`web.json` records the bound address and capability. `cx wake web` checks the
authenticated health endpoint before reusing that address. Stopping the owning
scheduler closes the listener and removes only its own metadata. All background
and browser actions retain the existing execution/state locks and timeout rules.

## Quota boundaries

cx controls when requests are attempted. It cannot reset an active server quota
window, prove that a request starts one, or read server quota from local token
totals. `success` means the CLI completed a turn. `+5h` is an arithmetic reference
only. Request cost follows the saved profile's authentication and selected model;
ephemeral requests are absent from local rollout usage reports.

The [official OpenAI documentation for non-interactive mode](https://developers.openai.com/codex/noninteractive/)
documents scheduled `codex exec` jobs, saved authentication, ephemeral execution,
and JSON completion events. The [configuration reference](https://developers.openai.com/codex/config-reference/)
documents sandbox and approval settings. The [usage guidance](https://developers.openai.com/codex/pricing/)
points to `/status` and the usage dashboard for actual limits and reset times;
availability of five-hour limits depends on the user's current plan.
