package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// runLogTimeFormat is the timestamp format used for each run.log line:
// millisecond-precision RFC3339, so lines sort lexically and stay readable.
const runLogTimeFormat = "2006-01-02T15:04:05.000Z07:00"

// RunLogger writes the on-disk log artifacts for a single run: one combined
// stdout/stderr file per launched process, the environment loader saw, a
// start/stop event log mirroring the TUI's activity feed, and a final file
// combining the run's config with its summary stats. A nil *RunLogger means
// logging is disabled; every method is a safe no-op in that case so callers
// don't need a separate enabled/disabled branch.
type RunLogger struct {
	dir string

	runLogMu sync.Mutex
	runLog   *os.File
}

// newRunLogger creates dir and writes environment.log into it, containing
// only the names listed in envVars that are actually set. An empty dir means
// logging is disabled, and returns (nil, nil).
func newRunLogger(dir string, envVars []string) (*RunLogger, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "run.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create run log: %w", err)
	}
	l := &RunLogger{dir: dir, runLog: f}
	if err := l.writeEnvironment(envVars); err != nil {
		return nil, err
	}
	return l, nil
}

// Dir returns the resolved log directory, or "" if l is nil.
func (l *RunLogger) Dir() string {
	if l == nil {
		return ""
	}
	return l.dir
}

// writeEnvironment writes environment.log containing "NAME=value" lines for
// each name in envVars that is set in the current process environment. Names
// that aren't set are silently omitted. If no lines result, the file is
// skipped entirely: no file means nothing was configured to be logged.
func (l *RunLogger) writeEnvironment(envVars []string) error {
	var lines []string
	for _, name := range envVars {
		if v, ok := os.LookupEnv(name); ok {
			lines = append(lines, name+"="+v)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	sort.Strings(lines)
	return os.WriteFile(filepath.Join(l.dir, "environment.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// processLogPath returns the path to the combined stdout/stderr file for
// launched process n.
func (l *RunLogger) processLogPath(n int64) string {
	return filepath.Join(l.dir, fmt.Sprintf("proc-%d.log", n))
}

// LogStart appends a start event for run attempt n to run.log. It's a no-op
// if l is nil.
func (l *RunLogger) LogStart(n int64, start time.Time) {
	if l == nil {
		return
	}
	l.writeRunLine(fmt.Sprintf("%s attempt=%d event=start\n", start.Format(runLogTimeFormat), n))
}

// LogStop appends a stop event for run attempt n to run.log, recording its
// exit code and duration. It's a no-op if l is nil.
func (l *RunLogger) LogStop(n int64, stop time.Time, exitCode int, d time.Duration) {
	if l == nil {
		return
	}
	l.writeRunLine(fmt.Sprintf("%s attempt=%d event=stop exit=%d duration=%s\n",
		stop.Format(runLogTimeFormat), n, exitCode, d.Round(time.Millisecond)))
}

func (l *RunLogger) writeRunLine(line string) {
	l.runLogMu.Lock()
	defer l.runLogMu.Unlock()
	if _, err := l.runLog.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write run log: %v\n", err)
	}
}

// Close closes the run log file. It's a no-op if l is nil.
func (l *RunLogger) Close() error {
	if l == nil {
		return nil
	}
	return l.runLog.Close()
}

// WriteSummary writes this run's config and final stats to summary.log. It's
// a no-op if l is nil.
func (l *RunLogger) WriteSummary(cfg Config, snap Snapshot) error {
	if l == nil {
		return nil
	}
	var b strings.Builder
	b.WriteString(FormatConfig(cfg))
	b.WriteString("\n")
	b.WriteString(FormatSummary(snap))
	return os.WriteFile(filepath.Join(l.dir, "summary.log"), []byte(b.String()), 0o644)
}

// FormatConfig renders the config options a run was launched with, in the
// same form shown by plain mode's startup header and written to a run's
// summary.log.
func FormatConfig(cfg Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "command:      %s\n", strings.Join(cfg.Args, " "))
	fmt.Fprintf(&b, "rate:         %v\n", cfg.Rate)
	fmt.Fprintf(&b, "max-parallel: %d\n", cfg.MaxParallel)
	if cfg.MaxCount > 0 {
		fmt.Fprintf(&b, "max-count:    %d\n", cfg.MaxCount)
	}
	if cfg.TestDuration > 0 {
		fmt.Fprintf(&b, "duration:     %v\n", cfg.TestDuration)
	}
	return b.String()
}
