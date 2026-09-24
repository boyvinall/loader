package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// logDirTimeFormat names a run's timestamped subfolder: a UTC, colon-free
// variant of RFC3339 that stays valid on filesystems (like NTFS) where ':'
// isn't allowed in path names.
const logDirTimeFormat = "2006-01-02T15-04-05Z"

// resolveLogDir returns the directory a run's log files should be written
// to: override (the --log-dir flag/LOADER_LOG_DIR value, or "" to use the
// default), with its own timestamped subfolder, so repeated runs never share
// a directory even when --log-dir is set.
func resolveLogDir(override string) string {
	base := override
	if base == "" {
		base = ".loader"
	}
	return filepath.Join(base, time.Now().UTC().Format(logDirTimeFormat))
}

func buildConfig(cmd *cli.Command, interactive bool) (Config, error) {
	args := cmd.Args().Slice()
	if len(args) == 0 {
		return Config{}, fmt.Errorf("command is required")
	}

	maxParallel := int(cmd.Int("max-parallel"))
	if maxParallel <= 0 {
		return Config{}, fmt.Errorf("--max-parallel must be greater than 0")
	}

	var logDir string
	if !cmd.Bool("no-log") {
		logDir = resolveLogDir(cmd.String("log-dir"))
	}

	return Config{
		Args:         args,
		Rate:         cmd.Duration("rate"),
		MaxParallel:  maxParallel,
		MaxCount:     int(cmd.Int("max-count")),
		TestDuration: cmd.Duration("duration"),
		OutputMode:   outputMode(interactive, cmd.Bool("verbose")),
		LogDir:       logDir,
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
			&cli.StringFlag{
				Name:    "log-dir",
				Usage:   "directory to write per-run log files into (gets its own timestamped subfolder); default: .loader",
				Sources: cli.EnvVars("LOADER_LOG_DIR"),
			},
			&cli.BoolFlag{
				Name:  "no-log",
				Usage: "disable writing per-run log files",
			},
		},
		Action: run,
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
