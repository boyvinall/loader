package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteEnvironmentFiltersToNamedVars(t *testing.T) {
	t.Setenv("LOADER_TEST_A", "1")
	t.Setenv("LOADER_TEST_B", "2")

	dir := t.TempDir()
	l := &RunLogger{dir: dir}
	if err := l.writeEnvironment([]string{"LOADER_TEST_B", "LOADER_TEST_A", "LOADER_TEST_UNSET"}); err != nil {
		t.Fatalf("writeEnvironment: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "environment.log"))
	if err != nil {
		t.Fatalf("read environment.log: %v", err)
	}
	want := "LOADER_TEST_A=1\nLOADER_TEST_B=2\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWriteEnvironmentSkipsFileWhenNoNames(t *testing.T) {
	dir := t.TempDir()
	l := &RunLogger{dir: dir}
	if err := l.writeEnvironment(nil); err != nil {
		t.Fatalf("writeEnvironment: %v", err)
	}
	assertNoEnvironmentLog(t, dir)
}

func TestWriteEnvironmentSkipsFileWhenNamesAllUnset(t *testing.T) {
	dir := t.TempDir()
	l := &RunLogger{dir: dir}
	if err := l.writeEnvironment([]string{"LOADER_TEST_DEFINITELY_UNSET"}); err != nil {
		t.Fatalf("writeEnvironment: %v", err)
	}
	assertNoEnvironmentLog(t, dir)
}

func assertNoEnvironmentLog(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "environment.log")); !os.IsNotExist(err) {
		t.Fatalf("expected environment.log to be absent, stat error: %v", err)
	}
}
