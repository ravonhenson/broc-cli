package verify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ravonhenson/drillbit/internal/backend"
	"github.com/ravonhenson/drillbit/internal/config"
	"github.com/ravonhenson/drillbit/internal/state"
)

// fakeBackend is an in-memory backend.Backend for exercising verify.Run
// without a real restic install.
type fakeBackend struct {
	snaps   []backend.Snapshot
	files   map[string][]backend.FileEntry
	content map[string]map[string][]byte // snapshotID -> path -> bytes
}

func (f *fakeBackend) Name() string { return "fake" }

func (f *fakeBackend) ListSnapshots(ctx context.Context) ([]backend.Snapshot, error) {
	return f.snaps, nil
}

func (f *fakeBackend) ListFiles(ctx context.Context, snapshotID string) ([]backend.FileEntry, error) {
	return f.files[snapshotID], nil
}

func (f *fakeBackend) RestoreFile(ctx context.Context, snapshotID, path, destDir string) (backend.RestoreResult, error) {
	data := f.content[snapshotID][path]
	local := filepath.Join(destDir, path)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return backend.RestoreResult{}, err
	}
	if err := os.WriteFile(local, data, 0o644); err != nil {
		return backend.RestoreResult{}, err
	}
	return backend.RestoreResult{LocalPath: local, Size: int64(len(data))}, nil
}

func TestRunBaselinesThenDetectsMismatch(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	fb := &fakeBackend{
		snaps: []backend.Snapshot{{ID: "s1"}},
		files: map[string][]backend.FileEntry{
			"s1": {{Path: "/a.txt", Type: "file", Size: 5}},
		},
		content: map[string]map[string][]byte{
			"s1": {"/a.txt": []byte("hello")},
		},
	}

	cfg := &config.Config{
		Name:    "test",
		Scratch: config.ScratchConfig{Dir: filepath.Join(dir, "scratch")},
		Sample:  config.SampleConfig{FilesPerRun: 5, ReverifyAfterDays: 0},
	}

	res, err := Run(context.Background(), cfg, fb, store)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.NewBaselines != 1 || !res.OK() {
		t.Fatalf("expected a clean baseline run, got %+v", res)
	}

	// Simulate the backend silently returning different bytes for the same
	// immutable (snapshot, path) on a later run - exactly the kind of
	// corruption drillbit exists to catch.
	fb.content["s1"]["/a.txt"] = []byte("corrupted-content")

	res2, err := Run(context.Background(), cfg, fb, store)
	if err != nil {
		t.Fatalf("Run (2nd): %v", err)
	}
	if res2.Mismatches != 1 || res2.OK() {
		t.Fatalf("expected the corruption to be detected, got %+v", res2)
	}

	rec, ok, err := store.Get("s1", "/a.txt")
	if err != nil || !ok {
		t.Fatalf("expected a ledger record, ok=%v err=%v", ok, err)
	}
	if rec.LastStatus != state.StatusMismatch {
		t.Fatalf("expected ledger to record mismatch, got %q", rec.LastStatus)
	}
}

func TestRunHandlesEmptyRepository(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	fb := &fakeBackend{}
	cfg := &config.Config{Name: "empty", Scratch: config.ScratchConfig{Dir: filepath.Join(dir, "scratch")}}

	res, err := Run(context.Background(), cfg, fb, store)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Files) != 0 || !res.OK() {
		t.Fatalf("expected an empty, OK result, got %+v", res)
	}
}
