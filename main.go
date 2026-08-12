package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"
)

// outputMode decides how subprocess stdout/stderr should be handled: the TUI
// always shows it (OutputCapture, for the log panel), regardless of
// --verbose; plain mode only shows it when --verbose is set, otherwise
// discards it.
func outputMode(interactive, verbose bool) OutputMode {
	switch {
	case interactive:
		return OutputCapture
	case verbose:
		return OutputPassthrough
	default:
		return OutputDiscard
	}
}

func buildConfig(cmd *cli.Command, interactive bool) (Config, error) {
	args := cmd.Args().Slice()
	if len(args) == 0 {
		return Config{}, fmt.Errorf("command is required")
	}

	rate := cmd.Duration("rate")
	if rate <= 0 {
		return Config{}, fmt.Errorf("--rate must be greater than 0")
	}
	maxParallel := int(cmd.Int("max-parallel"))
	if maxParallel <= 0 {
		return Config{}, fmt.Errorf("--max-parallel must be greater than 0")
	}

	return Config{
		Args:         args,
		Rate:         rate,
		MaxParallel:  maxParallel,
		MaxCount:     int(cmd.Int("max-count")),
		TestDuration: cmd.Duration("duration"),
		OutputMode:   outputMode(interactive, cmd.Bool("verbose")),
	}, nil
}

// isInteractiveTerminal reports whether both stdout and stderr are attached
// to a real terminal. When they aren't (piped, redirected, CI), the fullscreen
// TUI can't do anything useful, so the caller should fall back to plain-text
// output instead.
func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

func run(ctx context.Context, cmd *cli.Command) error {
	interactive := isInteractiveTerminal() && !cmd.Bool("no-tui")

	cfg, err := buildConfig(cmd, interactive)
	if err != nil {
		return err
	}

	if interactive {
		return runTUI(cfg)
	}
	return runPlain(cfg)
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
			&cli.BoolFlag{
				Name:  "no-tui",
				Usage: "force plain-text output instead of the fullscreen TUI",
			},
		},
		Action: run,
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
