package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setUserConfigDir points os.UserConfigDir() at a subdirectory of dir and
// returns the resulting config dir, accounting for the env var UserConfigDir
// consults varying by OS.
func setUserConfigDir(t *testing.T, dir string) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		t.Setenv("HOME", dir)
		return filepath.Join(dir, "Library", "Application Support")
	case "windows":
		t.Setenv("AppData", dir)
		return dir
	default:
		t.Setenv("XDG_CONFIG_HOME", dir)
		return dir
	}
}

func TestResolveLogEnvVarsFlagWinsOverAll(t *testing.T) {
	t.Setenv("LOADER_LOG_ENV_VARS", "FROM_ENV")
	writeConfigYAML(t, "FROM_YAML")

	got, err := resolveLogEnvVars([]string{"FROM_FLAG"})
	if err != nil {
		t.Fatalf("resolveLogEnvVars: %v", err)
	}
	if len(got) != 1 || got[0] != "FROM_FLAG" {
		t.Fatalf("got %v, want [FROM_FLAG]", got)
	}
}

func TestResolveLogEnvVarsCSVEnvVarOverYAML(t *testing.T) {
	t.Setenv("LOADER_LOG_ENV_VARS", " FOO , BAR ,,BAZ")
	writeConfigYAML(t, "FROM_YAML")

	got, err := resolveLogEnvVars(nil)
	if err != nil {
		t.Fatalf("resolveLogEnvVars: %v", err)
	}
	want := []string{"FOO", "BAR", "BAZ"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestResolveLogEnvVarsFallsBackToYAML(t *testing.T) {
	writeConfigYAML(t, "FROM_YAML")

	got, err := resolveLogEnvVars(nil)
	if err != nil {
		t.Fatalf("resolveLogEnvVars: %v", err)
	}
	if len(got) != 1 || got[0] != "FROM_YAML" {
		t.Fatalf("got %v, want [FROM_YAML]", got)
	}
}

func TestResolveLogEnvVarsNoSourceReturnsEmpty(t *testing.T) {
	setUserConfigDir(t, t.TempDir())

	got, err := resolveLogEnvVars(nil)
	if err != nil {
		t.Fatalf("resolveLogEnvVars: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestResolveLogEnvVarsMalformedYAMLErrors(t *testing.T) {
	configDir := setUserConfigDir(t, t.TempDir())
	configPath := filepath.Join(configDir, "loader")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configPath, "config.yaml"), []byte("logging: [this is not a map"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveLogEnvVars(nil); err == nil {
		t.Fatal("expected an error for malformed config.yaml, got nil")
	}
}

// writeConfigYAML points os.UserConfigDir() at a fresh temp dir and writes a
// loader/config.yaml into it with the given env var name.
func writeConfigYAML(t *testing.T, envVar string) {
	t.Helper()
	configDir := setUserConfigDir(t, t.TempDir())
	configPath := filepath.Join(configDir, "loader")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "logging:\n  env_vars:\n    - " + envVar + "\n"
	if err := os.WriteFile(filepath.Join(configPath, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
