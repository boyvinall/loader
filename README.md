# loader

A minimal CLI load-testing tool that runs a command repeatedly in parallel and reports timing statistics.

When run in an interactive terminal, `loader` shows a fullscreen dashboard (status/config,
live process output, latency stats, and a recent-activity feed). When stdout/stderr isn't a
terminal — piped, redirected to a file, or run in CI — it automatically falls back to the
plain-text output described below, so scripting and log capture keep working unchanged.

Note that this is deliberately barebones but easy to run for simple CLI tools.  If you want
something that can ramp up requests slowly, handle super-high load and back-off when it starts
getting errors, then this likely isn't the tool for you.  But if you want to run some simple
script in parallel a few times then perhaps it could help.

![loader demo](demo.gif)

## Install

```sh
go install github.com/boyvinall/loader@latest
```

Or build locally:

```sh
make build
```

## Usage

```
loader [options] COMMAND [ARGS...]
```

### Options

| Flag | Short | Default | Description |
|---|---|---|---|
| `--rate` | `-r` | `1s` | Interval between launching new commands |
| `--max-parallel` | `-p` | `20` | Maximum number of simultaneous processes |
| `--max-count` | `-n` | `0` | Total processes to launch before stopping (0 = unlimited) |
| `--duration` | `-d` | `0` | Stop launching after this duration (0 = unlimited) |
| `--verbose` | | off | Show stdout/stderr from each process (non-interactive mode) |
| `--non-interactive` | | off | Force plain-text output instead of the fullscreen TUI (for CI or agentic use) |
| `--status-interval` | | `5s` | Interval between status lines in non-interactive mode |
| `--output` | `-o` | `plain` | Output format in non-interactive mode: `plain` or `json` (JSON Lines on stdout) |
| `--log-dir` | | `.loader` | Directory to write per-run log files into (gets its own timestamped subfolder); also settable via `LOADER_LOG_DIR` |
| `--no-log` | | off | Disable writing per-run log files |
| `--log-env` | | none | Env var name to record in `environment.log` (can be specified multiple times); also settable via `LOADER_LOG_ENV_VARS` or a config file |

At least one of `--max-count` or `--duration` must be set, otherwise the tool runs until interrupted.

## Examples

Launch `curl` 100 times, at most 10 in parallel, one per second:

```sh
loader -n 100 -p 10 -- curl -s https://example.com
```

Run for 30 seconds at 5 launches per second, capped at 50 parallel:

```sh
loader -d 30s -r 200ms -p 50 ./my-script.sh
```

Stress test a local server with no rate limit between launches:

```sh
loader -d 60s -r 0s -p 100 -- curl -s http://localhost:8080/health
```

## Behaviour

- A new process is launched on every `--rate` tick. If all `--max-parallel` slots are occupied the launch loop blocks until one frees up.
- When `--duration` expires, no new processes are launched but any already-running processes are allowed to finish.
- **Ctrl-C once** (or `q`) — stops launching new processes, waits for running ones to finish.
- **Ctrl-C twice** (or `q` twice) — kills all running processes and finishes immediately.
- Subprocess stdout/stderr is discarded by default; use `--verbose` to see it.
- Each launched process inherits the environment plus:
  - `LOADER_RUN_ATTEMPT` — the 1-based launch counter for this process
  - `LOADER_RATE` — the configured `--rate` value
  - `LOADER_MAX_PARALLEL` — the configured `--max-parallel` value

## Log files

Unless `--no-log` is set, each run writes its log files to a fresh
timestamped subfolder (e.g. `2026-09-24T15-04-21Z`, UTC) under `--log-dir`
(default `.loader`), so repeated runs never clobber each other:

- `environment.log` — only the env vars you've chosen to record (see below), one `KEY=value`
  per line; omitted entirely if none are configured
- `proc-N.log` — combined stdout/stderr for launched process `N` (matches `LOADER_RUN_ATTEMPT`)
- `run.log` — one timestamped line per start/stop event, mirroring the TUI's recent-activity feed:

  ```
  2026-09-24T05:40:21.109+01:00 attempt=1 event=start
  2026-09-24T05:40:21.117+01:00 attempt=1 event=stop exit=1 duration=8ms
  ```

- `summary.log` — the run's config followed by its final summary stats, written once the run finishes

### Choosing which env vars to record

Which env vars end up in `environment.log` is resolved in this order (first match wins):

1. `--log-env NAME` (can be specified multiple times)
2. `LOADER_LOG_ENV_VARS` — a comma-separated list, e.g. `LOADER_LOG_ENV_VARS=FOO,BAR`
3. `logging.env_vars` in `~/.config/loader/config.yaml`:

   ```yaml
   logging:
     env_vars:
       - FOO
       - BAR
   ```

4. none — `environment.log` is omitted

Named vars that aren't actually set are silently skipped.

## Interactive mode

In a terminal, `loader` runs as a fullscreen dashboard with:

- a **status panel** — command, config, run stage, elapsed time, launched/running/completed/failed counts, and a progress bar toward `--max-count`/`--duration` when either is set;
- a **log panel** — streamed subprocess output (always shown, regardless of `--verbose`) and process errors, scrollable with the arrow keys, `pgup`/`pgdn`, or `end`/`G` to jump back to the tail; press `/` to filter it by a case-insensitive substring match (`enter` to apply, `esc` to cancel);
- a **latency panel** — live min/avg/p50/p95/p99/max, updated as processes complete;
- a **recent activity panel** — a rolling feed showing each process the moment it starts (`RUN`) and again once it completes (`OK`/`FAIL`, with duration).

Once the run finishes, the dashboard stays open showing the final results — press any key to exit.

## Output

A status line is printed to stderr on a timer during the run (one per line, not overwritten in
place — so this mode's output stays readable in CI logs or piped to a file):

```
[5s] launched=42      running=8       completed=34      failed=0
```

A line is only printed when the counters changed since the last one, except every 6th tick, which
is always printed so a log/agent watching for liveness still sees regular output even once nothing
is changing (e.g. while a handful of stragglers finish).

A summary is printed on exit:

```
=== Summary ===
Launched:  100
Completed: 100
Successes: 98
Failures:  2
Duration (OK):
  min: 142ms
  avg: 187ms
  p50: 183ms
  p95: 241ms
  p99: 267ms
  max: 312ms
Duration (FAIL):
  min: 95ms
  avg: 101ms
  p50: 101ms
  p95: 108ms
  p99: 108ms
  max: 108ms
```

### JSON output (`-o json`)

With `-o json`, non-interactive mode emits [JSON Lines](https://jsonlines.org/) on stdout instead
of the text above: one self-contained JSON object per line, each with a `type` field. The config
header and signal-handling notices ("Stopping launch loop...", "Waiting for running
processes...") are text-only concepts with no place in that schema, so they're suppressed
entirely — stdout is a clean stream a script or agent can parse directly.

A `status` line is emitted on the same timer, and with the same change-suppression/heartbeat
rules, as the plain status line:

```json
{"type":"status","elapsed_seconds":5.02,"launched":42,"running":8,"completed":34,"failed":0}
```

A `log` line is emitted for each process error, as it occurs (the same events the plain driver
prints as `[procID] text`):

```json
{"type":"log","elapsed_seconds":4.31,"proc_id":37,"text":"error after 812ms: exit status 1"}
```

A single `summary` line is emitted once at the end. `duration_ok`/`duration_fail` are omitted
when there's no data for that bucket (e.g. no failures):

```json
{"type":"summary","launched":100,"completed":100,"successes":98,"failures":2,"duration_ok":{"count":98,"min_ms":142,"avg_ms":187,"p50_ms":183,"p95_ms":241,"p99_ms":267,"max_ms":312},"duration_fail":{"count":2,"min_ms":95,"avg_ms":101,"p50_ms":101,"p95_ms":108,"p99_ms":108,"max_ms":108}}
```
