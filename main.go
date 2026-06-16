package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
)

type stats struct {
	mu        sync.Mutex
	durations []time.Duration
	launched  atomic.Int64
	completed atomic.Int64
	failed    atomic.Int64
}

func (s *stats) recordCompletion(d time.Duration, err error) {
	s.mu.Lock()
	s.durations = append(s.durations, d)
	s.mu.Unlock()
	s.completed.Add(1)
	if err != nil {
		s.failed.Add(1)
	}
}

func (s *stats) printSummary() {
	s.mu.Lock()
	defer s.mu.Unlock()

	launched := s.launched.Load()
	completed := s.completed.Load()
	failed := s.failed.Load()
	total := len(s.durations)

	fmt.Printf("\n=== Summary ===\n")
	fmt.Printf("Launched:  %d\n", launched)
	fmt.Printf("Completed: %d\n", completed)
	fmt.Printf("Successes: %d\n", completed-failed)
	fmt.Printf("Failures:  %d\n", failed)

	if total == 0 {
		return
	}

	slices.Sort(s.durations)

	var sum time.Duration
	for _, d := range s.durations {
		sum += d
	}
	avg := sum / time.Duration(total)

	pct := func(p float64) time.Duration {
		idx := max(int(math.Ceil(p/100.0*float64(total)))-1, 0)
		if idx >= total {
			idx = total - 1
		}
		return s.durations[idx]
	}

	fmt.Printf("Duration:\n")
	fmt.Printf("  min: %v\n", s.durations[0])
	fmt.Printf("  avg: %v\n", avg)
	fmt.Printf("  p50: %v\n", pct(50))
	fmt.Printf("  p95: %v\n", pct(95))
	fmt.Printf("  p99: %v\n", pct(99))
	fmt.Printf("  max: %v\n", s.durations[total-1])
}

func runLoadTest(ctx context.Context, cmd *cli.Command) error {
	args := cmd.Args().Slice()
	if len(args) == 0 {
		return fmt.Errorf("command is required")
	}

	rate := cmd.Duration("rate")
	maxParallel := int(cmd.Int("max-parallel"))
	maxCount := int(cmd.Int("max-count"))
	testDuration := cmd.Duration("duration")
	verbose := cmd.Bool("verbose")

	// Two-level signal handling:
	//   first Ctrl-C  → stop launching new processes
	//   second Ctrl-C → kill all running processes
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	launchCtx, cancelLaunch := context.WithCancel(context.Background())
	defer cancelLaunch()

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	if testDuration > 0 {
		var cancelTimeout context.CancelFunc
		launchCtx, cancelTimeout = context.WithTimeout(launchCtx, testDuration)
		defer cancelTimeout()
	}

	go func() {
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "\nStopping launch loop — press Ctrl-C again to kill running processes")
			cancelLaunch()
		case <-launchCtx.Done():
		}
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "\nKilling running processes")
			cancelRun()
		case <-runCtx.Done():
		}
	}()

	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	st := &stats{}

	// Status reporter: overwrites the current line every second on stderr.
	// Errors printed by worker goroutines prefix a newline to avoid overlap.
	stopStatus := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				launched := st.launched.Load()
				completed := st.completed.Load()
				failed := st.failed.Load()
				fmt.Fprintf(os.Stderr, "\rlaunched=%-6d  running=%-6d  completed=%-6d  failed=%-6d",
					launched, launched-completed, completed, failed)
			case <-stopStatus:
				return
			}
		}
	}()

	ticker := time.NewTicker(rate)
	defer ticker.Stop()

	fmt.Fprintf(os.Stderr, "command:      %s\n", strings.Join(args, " "))
	fmt.Fprintf(os.Stderr, "rate:         %v\n", rate)
	fmt.Fprintf(os.Stderr, "max-parallel: %d\n", maxParallel)
	if maxCount > 0 {
		fmt.Fprintf(os.Stderr, "max-count:    %d\n", maxCount)
	}
	if testDuration > 0 {
		fmt.Fprintf(os.Stderr, "duration:     %v\n", testDuration)
	}
	fmt.Fprintln(os.Stderr)

launchLoop:
	for {
		select {
		case <-launchCtx.Done():
			break launchLoop
		case <-ticker.C:
			if maxCount > 0 && int(st.launched.Load()) >= maxCount {
				break launchLoop
			}

			// Block until a parallel slot is free, or we're told to stop.
			select {
			case sem <- struct{}{}:
			case <-launchCtx.Done():
				break launchLoop
			}

			n := st.launched.Add(1)
			wg.Add(1)

			go func(n int64) {
				defer wg.Done()
				defer func() { <-sem }()

				start := time.Now()
				c := exec.CommandContext(runCtx, args[0], args[1:]...)
				c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if verbose {
					c.Stdout = os.Stdout
					c.Stderr = os.Stderr
				} else {
					c.Stdout = io.Discard
					c.Stderr = io.Discard
				}
				err := c.Run()
				elapsed := time.Since(start)
				st.recordCompletion(elapsed, err)

				if err != nil && runCtx.Err() == nil {
					fmt.Fprintf(os.Stderr, "\n[%d] error after %v: %v\n", n, elapsed, err)
				}
			}(n)
		}
	}

	close(stopStatus)
	fmt.Fprintln(os.Stderr, "\nWaiting for running processes to complete...")
	wg.Wait()

	st.printSummary()
	return nil
}

func main() {
	app := &cli.Command{
		Name:            "loader",
		Usage:           "run a command repeatedly in parallel as a load test",
		ArgsUsage:       "COMMAND [ARGS...]",
		HideHelpCommand: true,
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:    "rate",
				Aliases: []string{"r"},
				Usage:   "interval between launching new commands",
				Value:   time.Second,
			},
			&cli.IntFlag{
				Name:    "max-parallel",
				Aliases: []string{"p"},
				Usage:   "maximum number of parallel processes",
				Value:   20,
			},
			&cli.IntFlag{
				Name:    "max-count",
				Aliases: []string{"n"},
				Usage:   "maximum total processes to launch (0 = unlimited)",
				Value:   0,
			},
			&cli.DurationFlag{
				Name:    "duration",
				Aliases: []string{"d"},
				Usage:   "how long to keep launching processes (0 = unlimited)",
				Value:   0,
			},
			&cli.BoolFlag{
				Name:  "verbose",
				Usage: "show stdout/stderr from each process",
			},
		},
		Action: runLoadTest,
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
