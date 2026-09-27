package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
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

// logEnvVarsConfig mirrors the "logging.env_vars" section of
// ~/.config/loader/config.yaml.
type logEnvVarsConfig struct {
	Logging struct {
		EnvVars []string `yaml:"env_vars"`
	} `yaml:"logging"`
}

// resolveLogEnvVars resolves the environment variable names to record in
// environment.log, in order of precedence: the --log-env flag (if given at
// all, it wins outright), else the LOADER_LOG_ENV_VARS CSV env var, else
// ~/.config/loader/config.yaml's logging.env_vars, else none.
func resolveLogEnvVars(flagValues []string) ([]string, error) {
	if len(flagValues) > 0 {
		return flagValues, nil
	}

	if csv, ok := os.LookupEnv("LOADER_LOG_ENV_VARS"); ok {
		var names []string
		for _, name := range strings.Split(csv, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				names = append(names, name)
			}
		}
		return names, nil
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locate user config dir: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "loader", "config.yaml"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config.yaml: %w", err)
	}
	var cfg logEnvVarsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config.yaml: %w", err)
	}
	return cfg.Logging.EnvVars, nil
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
	var logEnvVars []string
	if !cmd.Bool("no-log") {
		logDir = resolveLogDir(cmd.String("log-dir"))

		var err error
		logEnvVars, err = resolveLogEnvVars(cmd.StringSlice("log-env"))
		if err != nil {
			return Config{}, err
		}
	}

	return Config{
		Args:         args,
		Rate:         cmd.Duration("rate"),
		MaxParallel:  maxParallel,
		MaxCount:     int(cmd.Int("max-count")),
		TestDuration: cmd.Duration("duration"),
		OutputMode:   outputMode(interactive, cmd.Bool("verbose")),
		LogDir:       logDir,
		LogEnvVars:   logEnvVars,
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
	interactive := isInteractiveTerminal() && !cmd.Bool("non-interactive")

	cfg, err := buildConfig(cmd, interactive)
	if err != nil {
		return err
	}

	if interactive {
		return runTUI(cfg)
	}

	format, err := ParseOutputFormat(cmd.String("output"))
	if err != nil {
		return err
	}
	statusInterval := cmd.Duration("status-interval")
	if statusInterval <= 0 {
		return fmt.Errorf("--status-interval must be greater than 0")
	}
	return runPlain(cfg, statusInterval, format)
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
				Name:  "non-interactive",
				Usage: "force plain-text output instead of the fullscreen TUI (for CI or agentic use)",
			},
			&cli.DurationFlag{
				Name:  "status-interval",
				Usage: "interval between status lines in non-interactive mode",
				Value: 5 * time.Second,
			},
			&cli.StringFlag{
				Name:    "output",
				Aliases: []string{"o"},
				Usage:   `output format in non-interactive mode: "plain" or "json" (JSON Lines on stdout)`,
				Value:   string(OutputFormatPlain),
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
			&cli.StringSliceFlag{
				Name:  "log-env",
				Usage: "environment variable name to record in environment.log (repeatable); overrides LOADER_LOG_ENV_VARS and config.yaml",
			},
		},
		Action: run,
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
