  # CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`loader` is a minimal CLI load-testing tool: it runs a given command repeatedly in parallel
and reports timing statistics. When run in an interactive terminal it shows a fullscreen
bubbletea dashboard; otherwise (piped, redirected, CI) it falls back to plain-text output on
stderr/stdout so scripting keeps working.

## Commands

```sh
make build      # go build -o loader .
make test       # go test ./...
make lint       # golangci-lint run
make tidy       # go mod tidy
make all        # lint, test, tidy, build (default target)
```

Run a single test: `go test ./... -run TestComputePercentilesKnownDistribution -v`

There's no other package to scope tests to — everything lives in `package main` at the repo
root.

## Architecture

Three files split cleanly along "engine vs. two interchangeable front-ends":

- **[engine.go](engine.go)** — `Engine`, the display-agnostic load-test runner. Owns launching
  processes at `Config.Rate` up to `Config.MaxParallel`, honoring `MaxCount`/`TestDuration`,
  and tracking results (counts, running-process set, duration history, a bounded activity
  feed). Exposes its state via `Snapshot()` (cheap, capped-sample percentiles — safe to poll
  every UI tick) and `FinalSnapshot()` (full-history percentiles, call once after `Run()`
  returns). Lifecycle is a one-way `Stage` progression (`Running → Stopping → Killing →
  Finished`) driven by `StopLaunching()`/`KillRunning()` (first/second Ctrl-C) and observable
  via the `Stopping()`/`Finished()` channels. `OutputMode` (`Discard`/`Passthrough`/`Capture`)
  is decided by the caller, not the engine — see `outputMode()` in [main.go](main.go).
- **[plain.go](plain.go)** — non-interactive driver: prints a config header, overwrites a
  status line on stderr once a second, streams "system" log lines (process errors) as they
  occur, and prints `FormatSummary()` on exit. Used whenever stdout/stderr isn't a real
  terminal, or `--no-tui` is passed.
- **[tui.go](tui.go)** — interactive driver: a bubbletea `Model` with five panels (Status,
  Config, Latency, Running, Log) plus a recent-Activity sidebar, driven by `Engine.Snapshot()`
  on a tick and by `Engine.LogLines()` for the log panel. Panel sizing is recalculated from
  terminal dimensions in `recalcSizes()`; layout math is the trickiest part of this file if
  something looks off after a resize.
- **[main.go](main.go)** — CLI flag definitions (`urfave/cli/v3`) and the interactive/plain
  dispatch: `isInteractiveTerminal()` checks stdout *and* stderr are TTYs, `--no-tui` forces
  plain mode even in a terminal.

Both drivers talk to `Engine` through the same public surface (`Run`, `Snapshot`,
`FinalSnapshot`, `LogLines`, `StopLaunching`, `KillRunning`, `Stopping`, `Finished`) — there is
no driver-specific state inside `Engine`. When changing engine behavior, check that both
`plain.go` and `tui.go` still make sense against the new semantics.

Key invariants worth knowing before touching `engine.go`:

- Sends to `Engine.logCh` (via `emitLog`) are always non-blocking; a full channel drops the
  line and increments `dropped` rather than stalling the load-generation path.
- `stats.mu` guards duration history and the recent-activity ring buffer; `stats.runningMu`
  separately guards the in-flight process map. `launched`/`completed`/`failed` are
  lock-free atomics. Don't conflate these — they're intentionally separate locks.
- `Snapshot()` percentiles are computed over at most `livePercentileSampleCap` most-recent
  samples; only `FinalSnapshot()` (called once, after `Run()` returns) uses the full history.
