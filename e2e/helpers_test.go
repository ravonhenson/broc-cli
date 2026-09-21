// Package e2e drives the real broc binary as a subprocess against real
// restic repositories, the same way a user actually invokes it. Unlike the
// unit and backend-integration tests, this is the layer that catches
// wiring bugs between packages (cobra flag plumbing, config file parsing,
// actual exit codes) that in-process tests can't see.
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ravonhenson/broc-cli/internal/config"
)

const testPassword = "e2e-test-password-123"

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "broc-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	binPath = filepath.Join(dir, "broc")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/broc")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building broc for e2e tests: %v\n%s\n", err, out)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Dir(wd) // e2e/ -> repo root
}

func requireRestic(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not found on PATH; skipping e2e test")
	}
}

type cliResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// runBroc executes the built binary with args in dir and captures its
// output and real exit code (as opposed to relying on the Go test process's
// own os.Exit machinery).
func runBroc(t *testing.T, dir string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running broc %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return cliResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}
}

// resticRepo initializes a real local restic repository under base/repo
// (matching writeConfig's default Restic.Repository) and returns its path
// and a source directory under base/src to back up from.
func resticRepo(t *testing.T, base string) (repoDir, srcDir string) {
	t.Helper()
	requireRestic(t)

	repoDir = filepath.Join(base, "repo")
	srcDir = filepath.Join(base, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("restic", "init")
	cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY="+repoDir, "RESTIC_PASSWORD="+testPassword)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restic init: %v\n%s", err, out)
	}
	return repoDir, srcDir
}

func resticBackup(t *testing.T, repoDir, srcDir string) {
	t.Helper()
	cmd := exec.Command("restic", "backup", srcDir)
	cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY="+repoDir, "RESTIC_PASSWORD="+testPassword)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restic backup: %v\n%s", err, out)
	}
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeConfig builds a broc config for a test, applying the same
// defaults `broc init` would, and saves it to <dir>/broc.yaml.
func writeConfig(t *testing.T, dir string, mutate func(*config.Config)) string {
	t.Helper()
	cfg := &config.Config{
		Name: "e2e",
		Restic: config.ResticConfig{
			Repository: filepath.Join(dir, "repo"),
			Env:        map[string]string{"RESTIC_PASSWORD": testPassword},
		},
		Scratch: config.ScratchConfig{Dir: filepath.Join(dir, "scratch")},
		Sample:  config.SampleConfig{FilesPerRun: 20, ReverifyAfterDays: 30},
		State:   config.StateConfig{Path: filepath.Join(dir, "state.db")},
	}
	if mutate != nil {
		mutate(cfg)
	}
	cfg.ApplyDefaults()
	path := filepath.Join(dir, "broc.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}
