package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drillbit.yaml")
	if err := os.WriteFile(path, []byte("name: myrepo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Backend != "restic" {
		t.Errorf("Backend default = %q, want restic", cfg.Backend)
	}
	if cfg.Restic.Binary != "restic" {
		t.Errorf("Restic.Binary default = %q, want restic", cfg.Restic.Binary)
	}
	if cfg.Sample.FilesPerRun != 15 {
		t.Errorf("FilesPerRun default = %d, want 15", cfg.Sample.FilesPerRun)
	}
	if cfg.Sample.ReverifyAfterDays != 30 {
		t.Errorf("ReverifyAfterDays default = %d, want 30", cfg.Sample.ReverifyAfterDays)
	}
	if !strings.Contains(cfg.Scratch.Dir, "myrepo") {
		t.Errorf("Scratch.Dir = %q, want it to derive from repo name", cfg.Scratch.Dir)
	}
	if !strings.Contains(cfg.State.Path, "myrepo") {
		t.Errorf("State.Path = %q, want it to derive from repo name", cfg.State.Path)
	}
}

func TestLoadPreservesExplicitValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drillbit.yaml")
	yaml := `
name: myrepo
backend: restic
restic:
  binary: /custom/restic
  repository: /tmp/repo
sample:
  files_per_run: 42
  max_file_size_mb: 100
  reverify_after_days: 7
scratch:
  dir: /custom/scratch
  keep: true
state:
  path: /custom/state.db
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Restic.Binary != "/custom/restic" {
		t.Errorf("Restic.Binary = %q, want /custom/restic", cfg.Restic.Binary)
	}
	if cfg.Sample.FilesPerRun != 42 {
		t.Errorf("FilesPerRun = %d, want 42", cfg.Sample.FilesPerRun)
	}
	if cfg.Scratch.Dir != "/custom/scratch" {
		t.Errorf("Scratch.Dir = %q, want /custom/scratch", cfg.Scratch.Dir)
	}
	if !cfg.Scratch.Keep {
		t.Error("Scratch.Keep = false, want true")
	}
	if cfg.State.Path != "/custom/state.db" {
		t.Errorf("State.Path = %q, want /custom/state.db", cfg.State.Path)
	}
	if cfg.MaxFileSize() != 100*1024*1024 {
		t.Errorf("MaxFileSize() = %d, want 100MB", cfg.MaxFileSize())
	}
	if cfg.ReverifyAfter().Hours() != 7*24 {
		t.Errorf("ReverifyAfter() = %v, want 168h", cfg.ReverifyAfter())
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/drillbit.yaml")
	if err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drillbit.yaml")
	if err := os.WriteFile(path, []byte("name: [unterminated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected a parse error for invalid YAML")
	}
}

func TestLoadMissingName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drillbit.yaml")
	if err := os.WriteFile(path, []byte("backend: restic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected a validation error for a missing name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error = %v, want it to mention the missing name", err)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "drillbit.yaml")

	cfg := &Config{
		Name:    "roundtrip",
		Backend: "restic",
		Restic:  ResticConfig{Repository: "/tmp/repo"},
		Sample:  SampleConfig{FilesPerRun: 5, ReverifyAfterDays: 10},
	}
	cfg.ApplyDefaults()

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if loaded.Name != cfg.Name || loaded.Restic.Repository != cfg.Restic.Repository {
		t.Errorf("round-tripped config = %+v, want to match %+v", loaded, cfg)
	}
	if loaded.Sample.FilesPerRun != 5 {
		t.Errorf("FilesPerRun after round-trip = %d, want 5", loaded.Sample.FilesPerRun)
	}
}

func TestReverifyAfterDaysZeroFallsBackToDefault(t *testing.T) {
	// YAML can't distinguish an explicitly-set 0 from an omitted int
	// field, so ApplyDefaults treats <=0 as "unset" and uses the default.
	// This means there is currently no config value that means "reverify
	// on every run" - documented behavior, not a bug; locked in here so a
	// future change to this is deliberate.
	cfg := &Config{Name: "x", Sample: SampleConfig{ReverifyAfterDays: 0}}
	cfg.ApplyDefaults()
	if cfg.Sample.ReverifyAfterDays != 30 {
		t.Errorf("ReverifyAfterDays = %d, want the default 30 when explicitly set to 0", cfg.Sample.ReverifyAfterDays)
	}
}

func TestSafeNameEmpty(t *testing.T) {
	if got := safeName(""); got != "default" {
		t.Errorf("safeName(\"\") = %q, want default", got)
	}
}
