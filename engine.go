package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// logChannelCapacity bounds how many pending log lines the engine will buffer
// before dropping. Sends to the channel are always non-blocking so that a slow
// or absent consumer (e.g. a TUI busy re-rendering) never stalls the
// load-generation path.
const logChannelCapacity = 2048

// recentActivityCap is the number of most-recent process starts/completions
// kept for the activity feed. The TUI's Activity panel only ever displays a
// terminal-height's worth of these at once; this just bounds how far back a
// resize (or a paging scroll, if that's ever added) can look.
const recentActivityCap = 200

// livePercentileSampleCap bounds how many of the most recent durations are
// sorted for a live Snapshot, so percentile computation stays cheap even
// under sustained high throughput. The final summary (FinalSnapshot) always
// uses the complete history.
const livePercentileSampleCap = 5000

// Stage represents the lifecycle of a load test run.
type Stage int32

const (
	StageRunning Stage = iota
	StageStopping
	StageKilling
	StageFinished
)

func (s Stage) String() string {
	switch s {
	case StageRunning:
		return "running"
	case StageStopping:
		return "stopping"
	case StageKilling:
		return "killing"
	case StageFinished:
		return "finished"
	default:
		return "unknown"
	}
}

// OutputMode controls what an Engine does with a launched process's
// stdout/stderr. It's decided by the caller (plain vs TUI mode, plus
// whether the user asked for --verbose) — the engine itself has no opinion
// on verbosity, only on how to route bytes once told.
type OutputMode int

const (
	// OutputDiscard throws subprocess stdout/stderr away. Used in plain mode
	// when --verbose isn't set.
	OutputDiscard OutputMode = iota
	// OutputPassthrough connects subprocess stdout/stderr directly to
	// os.Stdout/os.Stderr, byte-identical to a plain exec. Used in plain mode
	// when --verbose is set.
	OutputPassthrough
	// OutputCapture captures subprocess stdout/stderr line-by-line and routes
	// it through LogLines(). Used in TUI mode, always — the log panel shows
	// output regardless of --verbose.
	OutputCapture
)

// Config holds everything needed to run a load test, independent of how the
// results are displayed.
type Config struct {
	Args         []string
	Rate         time.Duration
	MaxParallel  int
	MaxCount     int
	TestDuration time.Duration

	// OutputMode controls how subprocess stdout/stderr is handled. See
	// OutputMode's docs.
	OutputMode OutputMode
}

// LogLine is a single line of output emitted by a process, or a system
// message (e.g. a process error), delivered via Engine.LogLines().
type LogLine struct {
	ProcID int64
	Stream string // "stdout", "stderr", or "system"
	Text   string
}

// ActivityKind distinguishes the two events an ActivityEntry can record: a
// process launching, or a process completing (successfully or not).
type ActivityKind int

const (
	ActivityStarted ActivityKind = iota
	ActivityOK
	ActivityFail
)

// ActivityEntry records a single event in the activity feed: either a
// process starting (Kind == ActivityStarted, Duration is zero) or a process
// completing (Kind == ActivityOK/ActivityFail, Duration set).
type ActivityEntry struct {
	Index    int64
	Time     time.Time
	Duration time.Duration
	Kind     ActivityKind
}

// RunningEntry describes a process that's currently in flight.
type RunningEntry struct {
	Index   int64
	Elapsed time.Duration
}

// Percentiles summarizes a set of process durations.
type Percentiles struct {
	Count                        int
	Min, Avg, P50, P95, P99, Max time.Duration
}

// Snapshot is a point-in-time, race-free view of the engine's state, safe to
// read from any goroutine.
type Snapshot struct {
	Stage           Stage
	Elapsed         time.Duration
	Launched        int64
	Running         int64
	Completed       int64
	Failed          int64
	RunningProcs    []RunningEntry
	Recent          []ActivityEntry
	PercentilesOK   Percentiles
	PercentilesFail Percentiles
	DroppedLogLines int64
}

// stats holds the mutable counters and history behind a Snapshot.
type stats struct {
	mu            sync.Mutex
	durationsOK   []time.Duration
	durationsFail []time.Duration
	recent        [recentActivityCap]ActivityEntry
	recentLen     int
	recentPos     int

	launched  atomic.Int64
	completed atomic.Int64
	failed    atomic.Int64

	runningMu sync.Mutex
	running   map[int64]time.Time
}

// startRunning records that process n began at start, for display in the
// running-processes panel.
func (s *stats) startRunning(n int64, start time.Time) {
	s.runningMu.Lock()
	if s.running == nil {
		s.running = make(map[int64]time.Time)
	}
	s.running[n] = start
	s.runningMu.Unlock()
}

// stopRunning removes process n from the running set once it completes.
func (s *stats) stopRunning(n int64) {
	s.runningMu.Lock()
	delete(s.running, n)
	s.runningMu.Unlock()
}

// snapshotRunning returns the currently in-flight processes, longest-running
// first.
func (s *stats) snapshotRunning() []RunningEntry {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()

	now := time.Now()
	out := make([]RunningEntry, 0, len(s.running))
	for n, start := range s.running {
		out = append(out, RunningEntry{Index: n, Elapsed: now.Sub(start)})
	}
	slices.SortFunc(out, func(a, b RunningEntry) int {
		switch {
		case a.Elapsed > b.Elapsed:
			return -1
		case a.Elapsed < b.Elapsed:
			return 1
		default:
			return 0
		}
	})
	return out
}

// appendRecentLocked appends e to the recent-activity ring buffer. Callers
// must hold s.mu.
func (s *stats) appendRecentLocked(e ActivityEntry) {
	s.recent[s.recentPos] = e
	s.recentPos = (s.recentPos + 1) % recentActivityCap
	if s.recentLen < recentActivityCap {
		s.recentLen++
	}
}

// recordStart appends a "started" entry to the activity feed for process n.
// It's the feed counterpart to startRunning, which tracks the same event for
// the Running panel's live process map.
func (s *stats) recordStart(n int64, start time.Time) {
	s.mu.Lock()
	s.appendRecentLocked(ActivityEntry{Index: n, Time: start, Kind: ActivityStarted})
	s.mu.Unlock()
}

func (s *stats) recordCompletion(n int64, d time.Duration, err error) {
	ok := err == nil
	now := time.Now()
	kind := ActivityOK
	if !ok {
		kind = ActivityFail
	}

	s.mu.Lock()
	if ok {
		s.durationsOK = append(s.durationsOK, d)
	} else {
		s.durationsFail = append(s.durationsFail, d)
	}
	s.appendRecentLocked(ActivityEntry{Index: n, Time: now, Duration: d, Kind: kind})
	s.mu.Unlock()

	s.completed.Add(1)
	if !ok {
		s.failed.Add(1)
	}
}

// snapshotRecentLocked returns the recent-activity ring buffer in
// oldest-to-newest order. Callers must hold s.mu.
func (s *stats) snapshotRecentLocked() []ActivityEntry {
	out := make([]ActivityEntry, 0, s.recentLen)
	if s.recentLen < recentActivityCap {
		out = append(out, s.recent[:s.recentLen]...)
		return out
	}
	out = append(out, s.recent[s.recentPos:]...)
	out = append(out, s.recent[:s.recentPos]...)
	return out
}

// capRecent returns the most recent livePercentileSampleCap elements of
// durations (or all of them, if fewer), so live percentile computation stays
// cheap under sustained high throughput regardless of how long the test has
// been running. Callers must hold stats.mu.
func capRecent(durations []time.Duration) []time.Duration {
	if len(durations) > livePercentileSampleCap {
		return durations[len(durations)-livePercentileSampleCap:]
	}
	return durations
}

// computePercentiles is a pure function computing min/avg/p50/p95/p99/max
// over a set of durations. It does not mutate durations.
func computePercentiles(durations []time.Duration) Percentiles {
	n := len(durations)
	if n == 0 {
		return Percentiles{}
	}

	sorted := make([]time.Duration, n)
	copy(sorted, durations)
	slices.Sort(sorted)

	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}

	pct := func(p float64) time.Duration {
		idx := max(int(math.Ceil(p/100.0*float64(n)))-1, 0)
		if idx >= n {
			idx = n - 1
		}
		return sorted[idx]
	}

	return Percentiles{
		Count: n,
		Min:   sorted[0],
		Avg:   sum / time.Duration(n),
		P50:   pct(50),
		P95:   pct(95),
		P99:   pct(99),
		Max:   sorted[n-1],
	}
}

// FormatSummary renders a Snapshot as the same summary text the plain-mode
// CLI has always printed on exit (without a leading blank line — callers
// that want one, as the original did, add it themselves).
func FormatSummary(snap Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== Summary ===\n")
	fmt.Fprintf(&b, "Launched:  %d\n", snap.Launched)
	fmt.Fprintf(&b, "Completed: %d\n", snap.Completed)
	fmt.Fprintf(&b, "Successes: %d\n", snap.Completed-snap.Failed)
	fmt.Fprintf(&b, "Failures:  %d\n", snap.Failed)

	formatDuration := func(label string, p Percentiles) {
		if p.Count == 0 {
			return
		}
		fmt.Fprintf(&b, "%s:\n", label)
		fmt.Fprintf(&b, "  min: %v\n", p.Min)
		fmt.Fprintf(&b, "  avg: %v\n", p.Avg)
		fmt.Fprintf(&b, "  p50: %v\n", p.P50)
		fmt.Fprintf(&b, "  p95: %v\n", p.P95)
		fmt.Fprintf(&b, "  p99: %v\n", p.P99)
		fmt.Fprintf(&b, "  max: %v\n", p.Max)
	}
	formatDuration("Duration (OK)", snap.PercentilesOK)
	formatDuration("Duration (FAIL)", snap.PercentilesFail)

	return b.String()
}

// lineWriter is an io.Writer that buffers partial writes and calls emit once
// per complete line. It is not safe for concurrent use by multiple writers,
// but exec.Cmd only ever calls Write from a single internal copying
// goroutine per stream, so each process gets its own lineWriter instance.
type lineWriter struct {
	buf  []byte
	emit func(string)
}

func newLineWriter(emit func(string)) *lineWriter {
	return &lineWriter{emit: emit}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.emit(line)
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush emits any trailing partial line left in the buffer (a process that
// exits without a final newline).
func (w *lineWriter) Flush() {
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

// Engine runs a load test: launching cfg.Args repeatedly in parallel at
// cfg.Rate, up to cfg.MaxParallel concurrent processes, honoring
// cfg.MaxCount/cfg.TestDuration, and tracking results. It is display-agnostic
// — plain.go and tui.go both drive it the same way.
type Engine struct {
	cfg   Config
	stats stats
	stage atomic.Int32

	logCh   chan LogLine
	dropped atomic.Int64

	startTime time.Time

	launchCtx     context.Context
	cancelLaunch  context.CancelFunc // explicit "stop launching" trigger
	cancelTimeout context.CancelFunc // internal cleanup for the duration timeout, if any

	runCtx    context.Context
	cancelRun context.CancelFunc // explicit "kill running processes" trigger

	stoppingCh   chan struct{}
	stoppingOnce sync.Once
	finishedCh   chan struct{}
	finishedOnce sync.Once
}

// NewEngine constructs an Engine ready to Run. Cancellation plumbing is set
// up eagerly so StopLaunching/KillRunning are safe to call as soon as
// NewEngine returns, even before Run's goroutine has started.
func NewEngine(cfg Config) *Engine {
	e := &Engine{
		cfg:        cfg,
		logCh:      make(chan LogLine, logChannelCapacity),
		stoppingCh: make(chan struct{}),
		finishedCh: make(chan struct{}),
	}

	outerCtx, cancelLaunch := context.WithCancel(context.Background())
	e.launchCtx = outerCtx
	e.cancelLaunch = cancelLaunch
	e.cancelTimeout = func() {}
	if cfg.TestDuration > 0 {
		e.launchCtx, e.cancelTimeout = context.WithTimeout(outerCtx, cfg.TestDuration)
	}

	e.runCtx, e.cancelRun = context.WithCancel(context.Background())
	e.stage.Store(int32(StageRunning))
	return e
}

// Stage returns the engine's current lifecycle stage.
func (e *Engine) Stage() Stage {
	return Stage(e.stage.Load())
}

// setStageAtLeast advances the stage, ignoring the request if the engine has
// already reached an equal-or-later stage (e.g. a stray StopLaunching call
// after KillRunning must not move Killing back to Stopping). It also closes
// the Stopping/Finished notification channels the first time each threshold
// is crossed.
func (e *Engine) setStageAtLeast(s Stage) {
	for {
		cur := Stage(e.stage.Load())
		if cur >= s {
			break
		}
		if e.stage.CompareAndSwap(int32(cur), int32(s)) {
			break
		}
	}
	if s >= StageStopping {
		e.stoppingOnce.Do(func() { close(e.stoppingCh) })
	}
	if s >= StageFinished {
		e.finishedOnce.Do(func() { close(e.finishedCh) })
	}
}

// Stopping returns a channel that's closed the moment the launch loop stops
// launching new processes, for any reason (explicit stop, max-count reached,
// or the test duration elapsing), while already-running processes may still
// be draining.
func (e *Engine) Stopping() <-chan struct{} {
	return e.stoppingCh
}

// Finished returns a channel that's closed once Run has returned (all
// processes have completed).
func (e *Engine) Finished() <-chan struct{} {
	return e.finishedCh
}

// StopLaunching stops the launch loop from starting new processes; any
// already-running processes are left to finish (first Ctrl-C).
func (e *Engine) StopLaunching() {
	e.setStageAtLeast(StageStopping)
	e.cancelLaunch()
}

// KillRunning stops the launch loop (if not already stopped) and kills all
// currently-running processes (second Ctrl-C).
func (e *Engine) KillRunning() {
	e.setStageAtLeast(StageKilling)
	e.cancelLaunch()
	e.cancelRun()
}

// WatchInterrupts starts a goroutine mapping os.Interrupt on sigCh onto e's
// two-stage stop/kill escalation: the first signal calls onStop (if non-nil)
// then StopLaunching; the second calls onKill (if non-nil) then KillRunning.
// Both plain.go and tui.go drive this identically — only what they do on
// each stage (print a message, or nothing) differs.
//
// The returned channel is closed once both stages have resolved, one way or
// another (a real signal, or the engine reaching Stopping/Finished on its
// own). Callers that need to react to further signals afterwards — see
// plain.go's third-Ctrl-C force-quit — can wait on it before resuming reads
// from sigCh themselves.
func (e *Engine) WatchInterrupts(sigCh <-chan os.Signal, onStop, onKill func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-sigCh:
			if onStop != nil {
				onStop()
			}
			e.StopLaunching()
		case <-e.Stopping():
		}
		select {
		case <-sigCh:
			if onKill != nil {
				onKill()
			}
			e.KillRunning()
		case <-e.Finished():
		}
	}()
	return done
}

// LogLines returns the channel of streamed log/system lines. It is closed
// once Run returns.
func (e *Engine) LogLines() <-chan LogLine {
	return e.logCh
}

func (e *Engine) emitLog(l LogLine) {
	select {
	case e.logCh <- l:
	default:
		e.dropped.Add(1)
	}
}

// Snapshot returns a race-free, point-in-time view of the engine's state.
// Live percentiles are computed from at most the most recent
// livePercentileSampleCap samples, to keep this cheap to call frequently
// (e.g. on a UI tick) regardless of how long the test has been running.
func (e *Engine) Snapshot() Snapshot {
	launched := e.stats.launched.Load()
	completed := e.stats.completed.Load()
	failed := e.stats.failed.Load()

	e.stats.mu.Lock()
	pctOK := computePercentiles(capRecent(e.stats.durationsOK))
	pctFail := computePercentiles(capRecent(e.stats.durationsFail))
	recent := e.stats.snapshotRecentLocked()
	e.stats.mu.Unlock()

	var elapsed time.Duration
	if !e.startTime.IsZero() {
		elapsed = time.Since(e.startTime)
	}

	// Running is derived from the actual running-process set rather than
	// launched-completed: those two counters are read from independent
	// atomics above, so a burst of completions between the two reads could
	// otherwise make completed briefly exceed launched and go negative.
	runningProcs := e.stats.snapshotRunning()

	return Snapshot{
		Stage:           e.Stage(),
		Elapsed:         elapsed,
		Launched:        launched,
		Running:         int64(len(runningProcs)),
		Completed:       completed,
		Failed:          failed,
		RunningProcs:    runningProcs,
		Recent:          recent,
		PercentilesOK:   pctOK,
		PercentilesFail: pctFail,
		DroppedLogLines: e.dropped.Load(),
	}
}

// FinalSnapshot is like Snapshot, but computes percentiles over the complete
// duration history rather than a capped sample. Intended to be called once,
// after Run has returned, for the final summary.
func (e *Engine) FinalSnapshot() Snapshot {
	snap := e.Snapshot()
	e.stats.mu.Lock()
	snap.PercentilesOK = computePercentiles(e.stats.durationsOK)
	snap.PercentilesFail = computePercentiles(e.stats.durationsFail)
	e.stats.mu.Unlock()
	return snap
}

// Run launches cfg.Args repeatedly until stopped, blocking until all
// launched processes have completed. Assumes cfg.Args is non-empty; callers
// validate that before constructing the Engine.
func (e *Engine) Run() {
	defer e.cancelLaunch()
	defer e.cancelTimeout()
	defer e.cancelRun()

	e.startTime = time.Now()

	sem := make(chan struct{}, e.cfg.MaxParallel)
	var wg sync.WaitGroup

	ticker := time.NewTicker(e.cfg.Rate)
	defer ticker.Stop()

launchLoop:
	for {
		if e.cfg.MaxCount > 0 && int(e.stats.launched.Load()) >= e.cfg.MaxCount {
			break launchLoop
		}

		// Block until a parallel slot is free, or we're told to stop.
		select {
		case sem <- struct{}{}:
		case <-e.launchCtx.Done():
			break launchLoop
		}

		n := e.stats.launched.Add(1)
		wg.Add(1)

		go func(n int64) {
			defer wg.Done()
			defer func() { <-sem }()
			e.launchOne(n)
		}(n)

		// The first launch fires immediately; every subsequent one waits
		// for the rate ticker.
		select {
		case <-e.launchCtx.Done():
			break launchLoop
		case <-ticker.C:
		}
	}

	e.setStageAtLeast(StageStopping)
	wg.Wait()
	e.setStageAtLeast(StageFinished)
	close(e.logCh)
}

func (e *Engine) launchOne(n int64) {
	start := time.Now()
	e.stats.startRunning(n, start)
	e.stats.recordStart(n, start)
	defer e.stats.stopRunning(n)

	c := exec.CommandContext(e.runCtx, e.cfg.Args[0], e.cfg.Args[1:]...)
	// Setpgid puts the launched command in its own process group (pgid ==
	// its pid) rather than loader's, so a terminal SIGINT is delivered only
	// to loader — never straight to the child — and our own two-stage
	// Ctrl-C handling stays in control of when the child dies. Cancel then
	// signals that whole group (not just the direct child) so grandchildren
	// spawned by the command are killed too; WaitDelay bounds how long Wait
	// will wait for stdout/stderr to drain if one of them lingers anyway.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	c.WaitDelay = 5 * time.Second

	var stdoutW, stderrW *lineWriter
	switch e.cfg.OutputMode {
	case OutputPassthrough:
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
	case OutputCapture:
		stdoutW = newLineWriter(func(line string) { e.emitLog(LogLine{ProcID: n, Stream: "stdout", Text: line}) })
		stderrW = newLineWriter(func(line string) { e.emitLog(LogLine{ProcID: n, Stream: "stderr", Text: line}) })
		c.Stdout = stdoutW
		c.Stderr = stderrW
	default: // OutputDiscard
		c.Stdout = io.Discard
		c.Stderr = io.Discard
	}

	err := c.Run()
	if stdoutW != nil {
		stdoutW.Flush()
		stderrW.Flush()
	}
	elapsed := time.Since(start)
	e.stats.recordCompletion(n, elapsed, err)

	if err != nil && e.runCtx.Err() == nil {
		e.emitLog(LogLine{ProcID: n, Stream: "system", Text: fmt.Sprintf("error after %v: %v", elapsed, err)})
	}
}
