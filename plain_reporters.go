package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// textReporter is the original human-readable plainReporter: config header,
// status, and errors go to stderr; the summary goes to stdout (so it can be
// captured separately from the rest).
type textReporter struct{}

func (textReporter) header(cfg Config) {
	fmt.Fprint(os.Stderr, FormatConfig(cfg))
	if cfg.LogDir != "" {
		fmt.Fprintf(os.Stderr, "log dir:      %s\n", cfg.LogDir)
	}
	fmt.Fprintln(os.Stderr)
}

func (textReporter) notice(msg string) {
	fmt.Fprintln(os.Stderr, msg)
}

func (textReporter) systemLog(_ time.Duration, procID int64, text string) {
	fmt.Fprintf(os.Stderr, "[%d] %s\n", procID, text)
}

func (textReporter) status(snap Snapshot) {
	fmt.Fprintf(os.Stderr, "[%s] launched=%-6d  running=%-6d  completed=%-6d  failed=%-6d\n",
		snap.Elapsed.Round(time.Second), snap.Launched, snap.Running, snap.Completed, snap.Failed)
}

func (textReporter) summary(snap Snapshot) {
	fmt.Print("\n" + FormatSummary(snap))
}

// jsonReporter renders runPlain's events as JSON Lines on stdout: one
// self-contained JSON object per line, discriminated by a "type" field. It
// only emits status, log, and summary events — the config header and
// signal-handling notices are text-only concepts with no place in that
// schema, so they're dropped rather than mixed in as a different shape.
type jsonReporter struct{}

func (jsonReporter) header(Config) {}
func (jsonReporter) notice(string) {}

func (jsonReporter) systemLog(elapsed time.Duration, procID int64, text string) {
	writeJSONLine(jsonLogEvent{
		Type:           "log",
		ElapsedSeconds: elapsed.Seconds(),
		ProcID:         procID,
		Text:           text,
	})
}

func (jsonReporter) status(snap Snapshot) {
	writeJSONLine(jsonStatusEvent{
		Type:           "status",
		ElapsedSeconds: snap.Elapsed.Seconds(),
		Launched:       snap.Launched,
		Running:        snap.Running,
		Completed:      snap.Completed,
		Failed:         snap.Failed,
	})
}

func (jsonReporter) summary(snap Snapshot) {
	writeJSONLine(jsonSummaryEvent{
		Type:         "summary",
		Launched:     snap.Launched,
		Completed:    snap.Completed,
		Successes:    snap.Completed - snap.Failed,
		Failures:     snap.Failed,
		DurationOK:   jsonPercentilesFromSnapshot(snap.PercentilesOK),
		DurationFail: jsonPercentilesFromSnapshot(snap.PercentilesFail),
	})
}

// jsonLogEvent is one "log" line of the JSONL stream: a process-error
// ("system") line, as also streamed to the TUI's log panel and, if logging
// is enabled, written to proc-N.log.
type jsonLogEvent struct {
	Type           string  `json:"type"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	ProcID         int64   `json:"proc_id"`
	Text           string  `json:"text"`
}

// jsonStatusEvent is one "status" line of the JSONL stream.
type jsonStatusEvent struct {
	Type           string  `json:"type"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	Launched       int64   `json:"launched"`
	Running        int64   `json:"running"`
	Completed      int64   `json:"completed"`
	Failed         int64   `json:"failed"`
}

// jsonSummaryEvent is the single "summary" line printed once at the end.
type jsonSummaryEvent struct {
	Type         string           `json:"type"`
	Launched     int64            `json:"launched"`
	Completed    int64            `json:"completed"`
	Successes    int64            `json:"successes"`
	Failures     int64            `json:"failures"`
	DurationOK   *jsonPercentiles `json:"duration_ok,omitempty"`
	DurationFail *jsonPercentiles `json:"duration_fail,omitempty"`
}

// jsonPercentiles mirrors Percentiles with durations in milliseconds, which
// is easier for a JSON consumer to work with than encoding/json's default
// time.Duration representation (nanoseconds as a plain number).
type jsonPercentiles struct {
	Count int     `json:"count"`
	MinMs float64 `json:"min_ms"`
	AvgMs float64 `json:"avg_ms"`
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
	MaxMs float64 `json:"max_ms"`
}

// jsonPercentilesFromSnapshot returns nil when there's no data for this
// bucket (e.g. no failures), so the summary event omits the field entirely
// rather than emitting misleading zeroes.
func jsonPercentilesFromSnapshot(p Percentiles) *jsonPercentiles {
	if p.Count == 0 {
		return nil
	}
	return &jsonPercentiles{
		Count: p.Count,
		MinMs: durationMs(p.Min),
		AvgMs: durationMs(p.Avg),
		P50Ms: durationMs(p.P50),
		P95Ms: durationMs(p.P95),
		P99Ms: durationMs(p.P99),
		MaxMs: durationMs(p.Max),
	}
}

func durationMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// jsonStdoutMu serialises writeJSONLine calls: the status ticker and system-log
// goroutines can both write concurrently, and without this a long line could
// interleave with another mid-write and corrupt both as JSON.
var jsonStdoutMu sync.Mutex

// writeJSONLine marshals v and writes it, newline-terminated, to stdout.
func writeJSONLine(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("jsonReporter: marshal %T: %v", v, err))
	}
	data = append(data, '\n')

	jsonStdoutMu.Lock()
	defer jsonStdoutMu.Unlock()
	_, _ = os.Stdout.Write(data)
}
