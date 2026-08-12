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
| `--verbose` | | off | Show stdout/stderr from each process (non-TUI mode) |
| `--no-tui` | | off | Force plain-text output instead of the fullscreen TUI |

At least one of `--max-count` or `--duration` must be set, otherwise the tool runs until interrupted.

## Examples

Launch `curl` 100 times, at most 10 in parallel, one per second:

```sh
loader -n 100 -p 10 curl -s https://example.com
```

Run for 30 seconds at 5 launches per second, capped at 50 parallel:

```sh
loader -d 30s -r 200ms -p 50 ./my-script.sh
```

Stress test a local server with no rate limit between launches:

```sh
loader -d 60s -r 0s -p 100 curl -s http://localhost:8080/health
```

## Behaviour

- A new process is launched on every `--rate` tick. If all `--max-parallel` slots are occupied the launch loop blocks until one frees up.
- When `--duration` expires, no new processes are launched but any already-running processes are allowed to finish.
- **Ctrl-C once** (or `q`) — stops launching new processes, waits for running ones to finish.
- **Ctrl-C twice** (or `q` twice) — kills all running processes and finishes immediately.
- Subprocess stdout/stderr is discarded by default; use `--verbose` to see it.

## Interactive mode

In a terminal, `loader` runs as a fullscreen dashboard with:

- a **status panel** — command, config, run stage, elapsed time, launched/running/completed/failed counts, and a progress bar toward `--max-count`/`--duration` when either is set;
- a **log panel** — streamed subprocess output (always shown, regardless of `--verbose`) and process errors, scrollable with the arrow keys, `pgup`/`pgdn`, or `end`/`G` to jump back to the tail;
- a **latency panel** — live min/avg/p50/p95/p99/max, updated as processes complete;
- a **recent activity panel** — a rolling feed showing each process the moment it starts (`RUN`) and again once it completes (`OK`/`FAIL`, with duration).

Once the run finishes, the dashboard stays open showing the final results — press any key to exit.

## Output

A live status line is printed to stderr during the run:

```
launched=42      running=8       completed=34      failed=0
```

A summary is printed on exit:

```
=== Summary ===
Launched:  100
Completed: 100
Successes: 98
Failures:  2
Duration:
  min: 142ms
  avg: 187ms
  p50: 183ms
  p95: 241ms
  p99: 267ms
  max: 312ms
```
