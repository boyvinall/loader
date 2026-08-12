package main

import (
	"testing"
	"time"
)

func TestComputePercentilesEmpty(t *testing.T) {
	got := computePercentiles(nil)
	if got.Count != 0 {
		t.Fatalf("Count = %d, want 0", got.Count)
	}
}

func TestComputePercentilesSingle(t *testing.T) {
	d := []time.Duration{42 * time.Millisecond}
	got := computePercentiles(d)
	want := Percentiles{Count: 1, Min: d[0], Avg: d[0], P50: d[0], P95: d[0], P99: d[0], Max: d[0]}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestComputePercentilesKnownDistribution(t *testing.T) {
	ms := time.Millisecond
	// Deliberately unsorted; computePercentiles must sort a copy without
	// mutating the input.
	in := []time.Duration{5 * ms, 3 * ms, 1 * ms, 4 * ms, 2 * ms}
	inCopy := append([]time.Duration(nil), in...)

	got := computePercentiles(in)

	if got.Count != 5 {
		t.Errorf("Count = %d, want 5", got.Count)
	}
	if got.Min != 1*ms {
		t.Errorf("Min = %v, want %v", got.Min, 1*ms)
	}
	if got.Max != 5*ms {
		t.Errorf("Max = %v, want %v", got.Max, 5*ms)
	}
	if got.Avg != 3*ms {
		t.Errorf("Avg = %v, want %v", got.Avg, 3*ms)
	}
	if got.P50 != 3*ms {
		t.Errorf("P50 = %v, want %v", got.P50, 3*ms)
	}
	if got.P95 != 5*ms {
		t.Errorf("P95 = %v, want %v", got.P95, 5*ms)
	}
	if got.P99 != 5*ms {
		t.Errorf("P99 = %v, want %v", got.P99, 5*ms)
	}

	for i := range in {
		if in[i] != inCopy[i] {
			t.Fatalf("computePercentiles mutated its input at index %d: got %v, want %v", i, in[i], inCopy[i])
		}
	}
}

func TestStatsRecentActivityRingBuffer(t *testing.T) {
	var s stats

	const total = recentActivityCap + 5
	for n := int64(1); n <= total; n++ {
		s.recordCompletion(n, time.Duration(n)*time.Millisecond, nil)
	}

	s.mu.Lock()
	recent := s.snapshotRecentLocked()
	s.mu.Unlock()

	if len(recent) != recentActivityCap {
		t.Fatalf("len(recent) = %d, want %d", len(recent), recentActivityCap)
	}

	// The oldest surviving entry should be #6 (1..5 evicted), the newest #25,
	// in chronological order.
	wantFirst := int64(total - recentActivityCap + 1)
	if recent[0].Index != wantFirst {
		t.Errorf("recent[0].Index = %d, want %d", recent[0].Index, wantFirst)
	}
	if recent[len(recent)-1].Index != total {
		t.Errorf("recent[last].Index = %d, want %d", recent[len(recent)-1].Index, total)
	}
	for i := 1; i < len(recent); i++ {
		if recent[i].Index != recent[i-1].Index+1 {
			t.Fatalf("recent activity out of order at %d: %d after %d", i, recent[i].Index, recent[i-1].Index)
		}
	}
}

func TestStatsRecordStartAppendsToFeed(t *testing.T) {
	var s stats

	start := time.Now()
	s.recordStart(1, start)
	s.recordStart(2, start.Add(time.Millisecond))

	s.mu.Lock()
	recent := s.snapshotRecentLocked()
	s.mu.Unlock()

	if len(recent) != 2 {
		t.Fatalf("len(recent) = %d, want 2", len(recent))
	}
	for i, e := range recent {
		if e.Kind != ActivityStarted {
			t.Errorf("recent[%d].Kind = %v, want ActivityStarted", i, e.Kind)
		}
		if e.Duration != 0 {
			t.Errorf("recent[%d].Duration = %v, want 0", i, e.Duration)
		}
	}
	if recent[0].Index != 1 || recent[1].Index != 2 {
		t.Errorf("recent indexes = [%d, %d], want [1, 2]", recent[0].Index, recent[1].Index)
	}
}

func TestLineWriterEmitsCompleteLines(t *testing.T) {
	var got []string
	w := newLineWriter(func(line string) { got = append(got, line) })

	n, err := w.Write([]byte("line one\nline two\n"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != len("line one\nline two\n") {
		t.Errorf("Write() n = %d, want %d", n, len("line one\nline two\n"))
	}

	want := []string{"line one", "line two"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLineWriterBuffersPartialLineAcrossWrites(t *testing.T) {
	var got []string
	w := newLineWriter(func(line string) { got = append(got, line) })

	if _, err := w.Write([]byte("hel")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want no emitted lines before a newline arrives", got)
	}

	if _, err := w.Write([]byte("lo\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("got %v, want [%q]", got, "hello")
	}
}

func TestLineWriterTrimsTrailingCR(t *testing.T) {
	var got []string
	w := newLineWriter(func(line string) { got = append(got, line) })

	if _, err := w.Write([]byte("crlf line\r\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if len(got) != 1 || got[0] != "crlf line" {
		t.Fatalf("got %v, want [%q]", got, "crlf line")
	}
}

func TestLineWriterFlushEmitsTrailingPartialLine(t *testing.T) {
	var got []string
	w := newLineWriter(func(line string) { got = append(got, line) })

	if _, err := w.Write([]byte("no trailing newline")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want no emitted lines before Flush", got)
	}

	w.Flush()
	if len(got) != 1 || got[0] != "no trailing newline" {
		t.Fatalf("got %v, want [%q]", got, "no trailing newline")
	}

	// Flush with nothing buffered is a no-op.
	w.Flush()
	if len(got) != 1 {
		t.Fatalf("got %v, want Flush on an empty buffer to emit nothing further", got)
	}
}

func TestEngineRunSuccess(t *testing.T) {
	cfg := Config{
		Args:        []string{"true"},
		Rate:        10 * time.Millisecond,
		MaxParallel: 5,
		MaxCount:    5,
	}
	eng := NewEngine(cfg)

	eng.Run()

	// LogLines is closed once Run returns; draining it should complete
	// immediately without blocking.
	for range eng.LogLines() {
	}

	snap := eng.FinalSnapshot()
	if snap.Stage != StageFinished {
		t.Errorf("Stage = %v, want %v", snap.Stage, StageFinished)
	}
	if snap.Launched != 5 {
		t.Errorf("Launched = %d, want 5", snap.Launched)
	}
	if snap.Completed != 5 {
		t.Errorf("Completed = %d, want 5", snap.Completed)
	}
	if snap.Failed != 0 {
		t.Errorf("Failed = %d, want 0", snap.Failed)
	}
	if snap.PercentilesOK.Count != 5 {
		t.Errorf("PercentilesOK.Count = %d, want 5", snap.PercentilesOK.Count)
	}
	if snap.PercentilesFail.Count != 0 {
		t.Errorf("PercentilesFail.Count = %d, want 0", snap.PercentilesFail.Count)
	}
}

func TestEngineRunFailure(t *testing.T) {
	cfg := Config{
		Args:        []string{"false"},
		Rate:        10 * time.Millisecond,
		MaxParallel: 3,
		MaxCount:    3,
	}
	eng := NewEngine(cfg)

	eng.Run()

	var systemLines int
	for l := range eng.LogLines() {
		if l.Stream == "system" {
			systemLines++
		}
	}
	if systemLines != 3 {
		t.Errorf("got %d system log lines, want 3", systemLines)
	}

	snap := eng.FinalSnapshot()
	if snap.Failed != 3 {
		t.Errorf("Failed = %d, want 3", snap.Failed)
	}
	if snap.Completed != 3 {
		t.Errorf("Completed = %d, want 3", snap.Completed)
	}
}

func TestEngineSnapshotRunningProcs(t *testing.T) {
	cfg := Config{
		Args:        []string{"sleep", "0.3"},
		Rate:        10 * time.Millisecond,
		MaxParallel: 3,
		MaxCount:    3,
	}
	eng := NewEngine(cfg)

	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.Run()
	}()

	// All 3 should have launched by now but none finished yet (they each
	// sleep 0.3s).
	time.Sleep(100 * time.Millisecond)
	mid := eng.Snapshot()
	if len(mid.RunningProcs) == 0 {
		t.Fatalf("expected running processes mid-run, got none (launched=%d completed=%d)",
			mid.Launched, mid.Completed)
	}
	for _, e := range mid.RunningProcs {
		if e.Elapsed <= 0 {
			t.Errorf("RunningEntry %+v has non-positive Elapsed", e)
		}
	}

	// None of the 3 processes have completed yet (they each sleep 0.3s), so
	// the activity feed should hold exactly 3 "started" entries and nothing
	// else.
	var started int
	for _, e := range mid.Recent {
		if e.Kind != ActivityStarted {
			t.Errorf("mid-run activity entry %+v has Kind %v, want ActivityStarted", e, e.Kind)
			continue
		}
		started++
	}
	if started != 3 {
		t.Errorf("got %d ActivityStarted entries mid-run, want 3", started)
	}

	<-done
	for range eng.LogLines() {
	}

	final := eng.Snapshot()
	if len(final.RunningProcs) != 0 {
		t.Errorf("expected no running processes after Run returned, got %d", len(final.RunningProcs))
	}
}
