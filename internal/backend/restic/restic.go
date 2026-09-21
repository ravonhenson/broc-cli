// Package restic implements the broc backend.Backend interface on top
// of the restic CLI, using its --json output.
//
// Notes on restic's JSON, since it's inconsistent across subcommands
// (checked against restic 0.16-0.19 docs):
//   - `restic snapshots --json` prints a single JSON array.
//   - `restic ls --json` prints NDJSON, one object per line, discriminated
//     by a "struct_type" field ("snapshot" for the leading summary line,
//     "node" for each file/dir entry).
//   - `restic restore --json` prints NDJSON discriminated by a different
//     field, "message_type" ("status", "verbose_status", "error", "summary").
package restic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ravonhenson/broc-cli/internal/backend"
)

func init() {
	backend.Register("restic", func(cfg map[string]any) (backend.Backend, error) {
		return newFromMap(cfg)
	})
}

// Config configures the restic backend directly (used by callers that
// already have typed config, bypassing the map-based registry factory).
type Config struct {
	Binary          string
	Repository      string
	PasswordCommand string
	PasswordFile    string
	ExtraArgs       []string
	Env             map[string]string
}

// Backend shells out to the restic binary.
type Backend struct {
	cfg Config
}

// New builds a restic Backend from typed config.
func New(cfg Config) *Backend {
	if cfg.Binary == "" {
		cfg.Binary = "restic"
	}
	return &Backend{cfg: cfg}
}

func newFromMap(m map[string]any) (*Backend, error) {
	get := func(k string) string {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	return New(Config{
		Binary:          get("binary"),
		Repository:      get("repository"),
		PasswordCommand: get("password_command"),
		PasswordFile:    get("password_file"),
	}), nil
}

func (b *Backend) Name() string { return "restic" }

func (b *Backend) command(ctx context.Context, args ...string) *exec.Cmd {
	full := append(append([]string{}, args...), b.cfg.ExtraArgs...)
	cmd := exec.CommandContext(ctx, b.cfg.Binary, full...)
	env := os.Environ()
	if b.cfg.Repository != "" {
		env = append(env, "RESTIC_REPOSITORY="+b.cfg.Repository)
	}
	if b.cfg.PasswordCommand != "" {
		env = append(env, "RESTIC_PASSWORD_COMMAND="+b.cfg.PasswordCommand)
	}
	if b.cfg.PasswordFile != "" {
		env = append(env, "RESTIC_PASSWORD_FILE="+b.cfg.PasswordFile)
	}
	for k, v := range b.cfg.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	return cmd
}

// runJSON runs restic and returns its stdout, erroring with stderr context
// on non-zero exit.
func (b *Backend) runJSON(ctx context.Context, args ...string) ([]byte, error) {
	cmd := b.command(ctx, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("restic %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

type snapshotJSON struct {
	Time    string   `json:"time"`
	ID      string   `json:"id"`
	ShortID string   `json:"short_id"`
	Paths   []string `json:"paths"`
	Tags    []string `json:"tags"`
}

func (s snapshotJSON) toSnapshot() backend.Snapshot {
	t, _ := time.Parse(time.RFC3339Nano, s.Time)
	return backend.Snapshot{
		ID:    s.ID,
		Short: s.ShortID,
		Time:  t,
		Paths: s.Paths,
		Tags:  s.Tags,
	}
}

// ListSnapshots implements backend.Backend.
func (b *Backend) ListSnapshots(ctx context.Context) ([]backend.Snapshot, error) {
	out, err := b.runJSON(ctx, "snapshots", "--json")
	if err != nil {
		return nil, err
	}
	var raw []snapshotJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parsing restic snapshots --json: %w", err)
	}
	snaps := make([]backend.Snapshot, 0, len(raw))
	for _, s := range raw {
		snaps = append(snaps, s.toSnapshot())
	}
	return snaps, nil
}

// lsLine covers both the leading "snapshot" struct_type line and the
// per-entry "node" lines from `restic ls --json`.
type lsLine struct {
	StructType string `json:"struct_type"`
	// node fields
	Name  string `json:"name"`
	Type  string `json:"type"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

// ListFiles implements backend.Backend.
func (b *Backend) ListFiles(ctx context.Context, snapshotID string) ([]backend.FileEntry, error) {
	cmd := b.command(ctx, "ls", snapshotID, "--json")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("restic ls %s: %w", snapshotID, err)
	}

	var files []backend.FileEntry
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var l lsLine
		if err := json.Unmarshal(line, &l); err != nil {
			continue // tolerate stray non-JSON/log lines
		}
		if l.StructType != "node" || l.Type != "file" {
			continue
		}
		mtime, _ := time.Parse(time.RFC3339Nano, l.Mtime)
		files = append(files, backend.FileEntry{
			Path:  l.Path,
			Size:  l.Size,
			Type:  l.Type,
			Mtime: mtime,
		})
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	if waitErr != nil {
		return nil, fmt.Errorf("restic ls %s: %w: %s", snapshotID, waitErr, strings.TrimSpace(stderr.String()))
	}
	if scanErr != nil && scanErr != io.EOF {
		return nil, fmt.Errorf("restic ls %s: reading output: %w", snapshotID, scanErr)
	}
	return files, nil
}

// restoreLine covers the "message_type"-discriminated lines from
// `restic restore --json`.
type restoreLine struct {
	MessageType string `json:"message_type"`
	Error       *struct {
		Message string `json:"message"`
	} `json:"error"`
	Item          string `json:"item"`
	FilesRestored int    `json:"files_restored"`
}

// RestoreFile implements backend.Backend. It restores exactly one file
// (identified by its exact snapshot-relative path as reported by
// ListFiles) into a fresh subdirectory of destDir, exercising the same
// restore path a real recovery would use.
func (b *Backend) RestoreFile(ctx context.Context, snapshotID, path, destDir string) (backend.RestoreResult, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return backend.RestoreResult{}, fmt.Errorf("creating scratch dir: %w", err)
	}

	cmd := b.command(ctx, "restore", snapshotID, "--target", destDir, "--include", path, "--json")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return backend.RestoreResult{}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return backend.RestoreResult{}, fmt.Errorf("restic restore %s %s: %w", snapshotID, path, err)
	}

	var restoreErrs []string
	restoredAny := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var l restoreLine
		if err := json.Unmarshal(line, &l); err != nil {
			continue
		}
		switch l.MessageType {
		case "error":
			if l.Error != nil {
				restoreErrs = append(restoreErrs, fmt.Sprintf("%s: %s", l.Item, l.Error.Message))
			}
		case "summary":
			if l.FilesRestored > 0 {
				restoredAny = true
			}
		}
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		return backend.RestoreResult{}, fmt.Errorf("restic restore %s %s: %w: %s", snapshotID, path, waitErr, strings.TrimSpace(stderr.String()))
	}
	if len(restoreErrs) > 0 {
		return backend.RestoreResult{}, fmt.Errorf("restic restore %s %s: %s", snapshotID, path, strings.Join(restoreErrs, "; "))
	}

	localPath := filepath.Join(destDir, path)
	info, err := os.Stat(localPath)
	if err != nil {
		return backend.RestoreResult{}, fmt.Errorf("restic reported success but restored file is missing at %s: %w", localPath, err)
	}
	_ = restoredAny // summary flag kept for future diagnostics; existence check above is authoritative
	return backend.RestoreResult{LocalPath: localPath, Size: info.Size()}, nil
}
