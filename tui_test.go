package main

import (
	"strings"
	"testing"
	"time"
)

func TestClampInt(t *testing.T) {
	tests := []struct {
		name      string
		v, lo, hi int
		want      int
	}{
		{"below range", -5, 0, 10, 0},
		{"above range", 15, 0, 10, 10},
		{"in range", 5, 0, 10, 5},
		{"equal to lo", 0, 0, 10, 0},
		{"equal to hi", 10, 0, 10, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampInt(tt.v, tt.lo, tt.hi); got != tt.want {
				t.Errorf("clampInt(%d, %d, %d) = %d, want %d", tt.v, tt.lo, tt.hi, got, tt.want)
			}
		})
	}
}

func TestTruncateEllipsis(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"fits exactly", "hello", 5, "hello"},
		{"shorter than width", "hi", 10, "hi"},
		{"zero width", "hello", 0, ""},
		{"negative width", "hello", -1, ""},
		{"width one", "hello", 1, "…"},
		{"truncated", "hello world", 8, "hello w…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateEllipsis(tt.s, tt.width); got != tt.want {
				t.Errorf("truncateEllipsis(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
		})
	}
}

func TestProgressFractionNoLimitConfigured(t *testing.T) {
	cfg := Config{}
	_, ok := progressFraction(cfg, Snapshot{})
	if ok {
		t.Fatalf("ok = true, want false when neither MaxCount nor TestDuration is set")
	}
}

func TestProgressFractionMaxCountOnly(t *testing.T) {
	cfg := Config{MaxCount: 10}
	frac, ok := progressFraction(cfg, Snapshot{Launched: 5})
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if frac != 0.5 {
		t.Errorf("frac = %v, want 0.5", frac)
	}
}

func TestProgressFractionDurationOnly(t *testing.T) {
	cfg := Config{TestDuration: 10 * time.Second}
	frac, ok := progressFraction(cfg, Snapshot{Elapsed: 3 * time.Second})
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if frac != 0.3 {
		t.Errorf("frac = %v, want 0.3", frac)
	}
}

func TestProgressFractionTakesLarger(t *testing.T) {
	cfg := Config{MaxCount: 10, TestDuration: 10 * time.Second}
	// 20% by count, 80% by duration — should report the larger.
	frac, ok := progressFraction(cfg, Snapshot{Launched: 2, Elapsed: 8 * time.Second})
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if frac != 0.8 {
		t.Errorf("frac = %v, want 0.8 (the larger of the two fractions)", frac)
	}
}

func TestProgressFractionClampedToOne(t *testing.T) {
	cfg := Config{MaxCount: 10}
	frac, ok := progressFraction(cfg, Snapshot{Launched: 999})
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if frac != 1 {
		t.Errorf("frac = %v, want 1 (clamped)", frac)
	}
}

func TestLatencyCellNoSamples(t *testing.T) {
	if got := latencyCell(Percentiles{}, 0); got != "-" {
		t.Errorf("latencyCell with Count=0 = %q, want %q", got, "-")
	}
}

func TestLatencyCellWithSamples(t *testing.T) {
	p := Percentiles{Count: 1}
	got := latencyCell(p, 1500*time.Microsecond)
	want := (1500 * time.Microsecond).Round(time.Millisecond).String()
	if got != want {
		t.Errorf("latencyCell = %q, want %q", got, want)
	}
}

func TestFormatLogLine(t *testing.T) {
	tests := []struct {
		stream string
	}{
		{"system"}, {"stdout"}, {"stderr"},
	}
	for _, tt := range tests {
		t.Run(tt.stream, func(t *testing.T) {
			l := LogLine{ProcID: 7, Stream: tt.stream, Text: "hello"}
			got := formatLogLine(l)
			if !strings.Contains(got, "[7]") || !strings.Contains(got, "hello") {
				t.Errorf("formatLogLine(%+v) = %q, want it to contain proc id and text", l, got)
			}
		})
	}
}
