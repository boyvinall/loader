package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"
)

// runPlain drives an Engine and reports progress the way the CLI has always
// behaved: a config header, a live status line overwritten on stderr once a
// second, error lines printed as they occur, and a final summary. Used when
// stdout/stderr isn't an interactive terminal.
func runPlain(cfg Config) error {
	eng := NewEngine(cfg)

	fmt.Fprintf(os.Stderr, "command:      %s\n", strings.Join(cfg.Args, " "))
	fmt.Fprintf(os.Stderr, "rate:         %v\n", cfg.Rate)
	fmt.Fprintf(os.Stderr, "max-parallel: %d\n", cfg.MaxParallel)
	if cfg.MaxCount > 0 {
		fmt.Fprintf(os.Stderr, "max-count:    %d\n", cfg.MaxCount)
	}
	if cfg.TestDuration > 0 {
		fmt.Fprintf(os.Stderr, "duration:     %v\n", cfg.TestDuration)
	}
	fmt.Fprintln(os.Stderr)

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
			fmt.Fprintln(os.Stderr, "\nStopping launch loop — press Ctrl-C again to kill running processes")
		},
		func() {
			fmt.Fprintln(os.Stderr, "\nKilling running processes")
		},
	)
	forceQuit := make(chan struct{})
	go func() {
		<-stopKillDone
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "\nForcing exit — running processes may be left behind")
			close(forceQuit)
		case <-eng.Finished():
		}
	}()

	// Status reporter: overwrites the current line every second on stderr.
	// Errors printed by the log-line goroutine prefix a newline to avoid
	// overlap.
	statusDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				snap := eng.Snapshot()
				fmt.Fprintf(os.Stderr, "\rlaunched=%-6d  running=%-6d  completed=%-6d  failed=%-6d",
					snap.Launched, snap.Running, snap.Completed, snap.Failed)
			case <-statusDone:
				return
			}
		}
	}()

	// In plain mode, OutputMode is never OutputCapture, so LogLines() only
	// ever carries "system" lines (process errors) — verbose stdout/stderr
	// goes straight to os.Stdout/os.Stderr in Engine.launchOne
	// (OutputPassthrough) or is discarded (OutputDiscard).
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		for line := range eng.LogLines() {
			if line.Stream == "system" {
				fmt.Fprintf(os.Stderr, "\n[%d] %s\n", line.ProcID, line.Text)
			}
		}
	}()

	runDone := make(chan struct{})
	go func() {
		eng.Run()
		close(runDone)
	}()

	<-eng.Stopping()
	close(statusDone)
	fmt.Fprintln(os.Stderr, "\nWaiting for running processes to complete...")

	select {
	case <-runDone:
	case <-forceQuit:
		return nil
	}
	<-logDone

	// The summary has always gone to stdout (unlike the config header and
	// status line, which go to stderr) so it can be captured separately.
	fmt.Print("\n" + FormatSummary(eng.FinalSnapshot()))
	return nil
}
