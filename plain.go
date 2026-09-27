package main

import (
	"fmt"
	"os"
	"os/signal"
	"time"
)

// statusHeartbeatEvery is how many status-interval ticks pass between forced
// status lines, so a consumer watching the log for liveness sees a line even
// when nothing has changed since the last tick.
const statusHeartbeatEvery = 6

// OutputFormat selects how runPlain renders its output.
type OutputFormat string

const (
	OutputFormatPlain OutputFormat = "plain"
	OutputFormatJSON  OutputFormat = "json"
)

// ParseOutputFormat validates a --output flag value.
func ParseOutputFormat(s string) (OutputFormat, error) {
	switch f := OutputFormat(s); f {
	case OutputFormatPlain, OutputFormatJSON:
		return f, nil
	default:
		return "", fmt.Errorf("invalid --output value %q (want %q or %q)", s, OutputFormatPlain, OutputFormatJSON)
	}
}

// plainReporter renders the events runPlain produces. textReporter and
// jsonReporter are the two implementations; everything else in this file is
// oblivious to which one is in use.
type plainReporter interface {
	// header prints the config/log-dir header shown once at startup.
	header(cfg Config)
	// notice prints a one-off human-readable message: a signal-handling
	// transition or the final "waiting for processes" line.
	notice(msg string)
	// systemLog prints a process-error line as it occurs.
	systemLog(elapsed time.Duration, procID int64, text string)
	// status prints a periodic progress line.
	status(snap Snapshot)
	// summary prints the final results.
	summary(snap Snapshot)
}

func newPlainReporter(format OutputFormat) plainReporter {
	if format == OutputFormatJSON {
		return jsonReporter{}
	}
	return textReporter{}
}

// runPlain drives an Engine and reports progress the way the CLI has always
// behaved: a config header, a status line printed to stderr on a timer,
// error lines printed as they occur, and a final summary. Used when
// stdout/stderr isn't an interactive terminal.
func runPlain(cfg Config, statusInterval time.Duration, format OutputFormat) error {
	eng, err := NewEngine(cfg)
	if err != nil {
		return err
	}
	reporter := newPlainReporter(format)

	reporter.header(cfg)

	// Three-level signal handling:
	//   first Ctrl-C  → stop launching new processes
	//   second Ctrl-C → kill all running processes
	//   third Ctrl-C  → give up waiting and exit now, even if some killed
	//                   processes haven't exited yet (mirrors the TUI's
	//                   safety valve for the same situation)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	stopKillDone := eng.WatchInterrupts(sigCh,
		func() {
			reporter.notice("Stopping launch loop — press Ctrl-C again to kill running processes")
		},
		func() {
			reporter.notice("Killing running processes")
		},
	)
	forceQuit := watchForceQuit(eng, sigCh, stopKillDone, reporter)

	statusDone := make(chan struct{})
	go runStatusTicker(eng, statusInterval, reporter, statusDone)

	logDone := make(chan struct{})
	go streamSystemLog(eng, reporter, logDone)

	runDone := make(chan struct{})
	go func() {
		eng.Run()
		close(runDone)
	}()

	<-eng.Stopping()
	close(statusDone)
	reporter.notice("Waiting for running processes to complete...")

	select {
	case <-runDone:
	case <-forceQuit:
		return nil
	}
	<-logDone

	reporter.summary(eng.FinalSnapshot())
	return nil
}

// watchForceQuit waits for the stop/kill sequence to finish, then watches for
// a third Ctrl-C (or the engine finishing on its own): given one, it reports
// and closes the returned channel so runPlain can give up waiting immediately.
func watchForceQuit(eng *Engine, sigCh <-chan os.Signal, stopKillDone <-chan struct{}, reporter plainReporter) <-chan struct{} {
	forceQuit := make(chan struct{})
	go func() {
		<-stopKillDone
		select {
		case <-sigCh:
			reporter.notice("Forcing exit — running processes may be left behind")
			close(forceQuit)
		case <-eng.Finished():
		}
	}()
	return forceQuit
}

// runStatusTicker prints one status line per tick until done is closed. To
// keep a long-running test's output from filling up with duplicate lines, a
// tick is only printed when the counters changed since the last one printed,
// except every statusHeartbeatEvery-th tick, which is always printed so a
// log/agent watching for liveness still sees regular output.
func runStatusTicker(eng *Engine, interval time.Duration, reporter plainReporter, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last Snapshot
	havePrinted := false
	ticks := 0
	for {
		select {
		case <-ticker.C:
			ticks++
			snap := eng.Snapshot()
			changed := !havePrinted ||
				snap.Launched != last.Launched ||
				snap.Running != last.Running ||
				snap.Completed != last.Completed ||
				snap.Failed != last.Failed
			if changed || ticks%statusHeartbeatEvery == 0 {
				reporter.status(snap)
				last = snap
				havePrinted = true
			}
		case <-done:
			return
		}
	}
}

// streamSystemLog reports each "system" line (a process error) as it occurs,
// closing done once the engine closes its log-line channel. In plain mode,
// OutputMode is never OutputCapture, so LogLines() only ever carries "system"
// lines — verbose stdout/stderr goes straight to os.Stdout/os.Stderr in
// Engine.launchOne (OutputPassthrough) or is discarded (OutputDiscard).
func streamSystemLog(eng *Engine, reporter plainReporter, done chan<- struct{}) {
	defer close(done)
	for line := range eng.LogLines() {
		if line.Stream == "system" {
			reporter.systemLog(eng.Elapsed(), line.ProcID, line.Text)
		}
	}
}
