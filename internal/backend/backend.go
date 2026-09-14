// Package backend defines the interface drillbit uses to talk to a backup
// tool (restic, and later borg/kopia). Each concrete backend translates
// these calls into that tool's CLI/API.
package backend

import (
	"context"
	"errors"
	"time"
)

// ErrNotSupported is returned by optional backend capabilities that a given
// implementation does not (yet) provide.
var ErrNotSupported = errors.New("backend: operation not supported")

// Snapshot describes one point-in-time backup.
type Snapshot struct {
	ID    string
	Short string // short/display ID, if the backend has one distinct from ID
	Time  time.Time
	Paths []string
	Tags  []string
}

// FileEntry describes one file known to a snapshot, without restoring it.
type FileEntry struct {
	Path  string
	Size  int64
	Type  string // "file", "dir", "symlink"
	Mtime time.Time
}

// RestoreResult is the outcome of restoring a single file into scratch space.
type RestoreResult struct {
	// LocalPath is where the restored file landed on disk.
	LocalPath string
	// Size is the restored file's size in bytes.
	Size int64
}

// Backend is implemented by each supported backup tool.
type Backend interface {
	// Name identifies the backend, e.g. "restic".
	Name() string

	// ListSnapshots returns all snapshots currently visible in the
	// configured repository, newest last.
	ListSnapshots(ctx context.Context) ([]Snapshot, error)

	// ListFiles returns the regular files contained in a snapshot, without
	// restoring any of them. Used to build the sampling population cheaply.
	ListFiles(ctx context.Context, snapshotID string) ([]FileEntry, error)

	// RestoreFile restores exactly one file from a snapshot into destDir,
	// exercising the same restore path a real recovery would use.
	RestoreFile(ctx context.Context, snapshotID, path, destDir string) (RestoreResult, error)
}
