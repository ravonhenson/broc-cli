package verify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ravonhenson/broc-cli/internal/backend"
	"github.com/ravonhenson/broc-cli/internal/config"
	"github.com/ravonhenson/broc-cli/internal/state"
)

// fakeBackend is an in-memory backend.Backend for exercising verify.Run
// without a real restic install.
type fakeBackend struct {
	snaps   []backend.Snapshot
	files   map[string][]backend.FileEntry
	content map[string]map[string][]byte // snapshotID -> path -> bytes

	// restoreErr, if set for a path, makes RestoreFile fail for it.
	restoreErr map[string]error
	// missingContent, if true, reports success without writing the file,
	// simulating a restored-but-then-vanished/unreadable file.
	missingContent bool
}

func (f *fakeBackend) Name() string { return "fake" }

func (f *fakeBackend) ListSnapshots(ctx context.Context) ([]backend.Snapshot, error) {
	return f.snaps, nil
}

func (f *fakeBackend) ListFiles(ctx context.Context, snapshotID string) ([]backend.FileEntry, error) {
	return f.files[snapshotID], nil
}

func (f *fakeBackend) RestoreFile(ctx context.Context, snapshotID, path, destDir string) (backend.RestoreResult, error) {
	if err := f.restoreErr[path]; err != nil {
		return backend.RestoreResult{}, err
	}
	local := filepath.Join(destDir, path)
	if f.missingContent {
		return backend.RestoreResult{LocalPath: local, Size: 0}, nil
	}
	data := f.content[snapshotID][path]
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
	// corruption broc exists to catch.
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

func TestRunRecordsErrorOnRestoreFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	fb := &fakeBackend{
		snaps: []backend.Snapshot{{ID: "s1"}},
		files: map[string][]backend.FileEntry{
			"s1": {{Path: "/broken.txt", Type: "file", Size: 5}},
		},
		content:    map[string]map[string][]byte{"s1": {"/broken.txt": []byte("hello")}},
		restoreErr: map[string]error{"/broken.txt": errors.New("simulated restore failure")},
	}
	cfg := &config.Config{Name: "test", Scratch: config.ScratchConfig{Dir: filepath.Join(dir, "scratch")}, Sample: config.SampleConfig{FilesPerRun: 5}}

	res, err := Run(context.Background(), cfg, fb, store)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Errors != 1 || res.OK() {
		t.Fatalf("expected 1 error and a failed run, got %+v", res)
	}
	rec, ok, err := store.Get("s1", "/broken.txt")
	if err != nil || !ok {
		t.Fatalf("expected a ledger record even on failure, ok=%v err=%v", ok, err)
	}
	if rec.LastStatus != state.StatusError || !strings.Contains(rec.LastError, "simulated restore failure") {
		t.Errorf("ledger record = %+v, want it to capture the restore error", rec)
	}
}

func TestRunRecordsErrorWhenRestoredFileIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	fb := &fakeBackend{
		snaps:          []backend.Snapshot{{ID: "s1"}},
		files:          map[string][]backend.FileEntry{"s1": {{Path: "/ghost.txt", Type: "file", Size: 5}}},
		missingContent: true,
	}
	cfg := &config.Config{Name: "test", Scratch: config.ScratchConfig{Dir: filepath.Join(dir, "scratch")}, Sample: config.SampleConfig{FilesPerRun: 5}}

	res, err := Run(context.Background(), cfg, fb, store)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Errors != 1 || res.OK() {
		t.Fatalf("expected a hashing error when the restored file doesn't exist, got %+v", res)
	}
	if !strings.Contains(res.Files[0].Error, "hashing restored file") {
		t.Errorf("FileResult.Error = %q, want it to mention hashing failure", res.Files[0].Error)
	}
}

func TestChooseSnapshotsCapsAndKeepsNewest(t *testing.T) {
	all := make([]backend.Snapshot, 30)
	for i := range all {
		all[i] = backend.Snapshot{ID: fmt.Sprintf("s%02d", i)}
	}
	newest := all[len(all)-1]

	chosen := chooseSnapshots(all, 10)
	if len(chosen) != 10 {
		t.Fatalf("got %d snapshots, want 10", len(chosen))
	}
	foundNewest := false
	seen := map[string]bool{}
	for _, s := range chosen {
		if s.ID == newest.ID {
			foundNewest = true
		}
		if seen[s.ID] {
			t.Fatalf("duplicate snapshot %s in chosen set", s.ID)
		}
		seen[s.ID] = true
	}
	if !foundNewest {
		t.Error("expected the newest snapshot to always be included")
	}
}

func TestChooseSnapshotsNoCapReturnsAllUnchanged(t *testing.T) {
	all := []backend.Snapshot{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got := chooseSnapshots(all, 0)
	if len(got) != 3 {
		t.Fatalf("got %d snapshots with max=0 (no cap), want all 3", len(got))
	}

	got = chooseSnapshots(all, 100)
	if len(got) != 3 {
		t.Fatalf("got %d snapshots with max larger than input, want all 3", len(got))
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
