# loader

A minimal CLI load-testing tool that runs a command repeatedly in parallel and reports timing statistics.

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
| `--verbose` | | off | Show stdout/stderr from each process |

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
- **Ctrl-C once** — stops launching new processes, waits for running ones to finish.
- **Ctrl-C twice** — sends SIGKILL to all running processes and exits immediately.
- Subprocess stdout/stderr is discarded by default; use `--verbose` to see it.

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
