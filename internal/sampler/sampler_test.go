package sampler

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ravonhenson/broc-cli/internal/backend"
	"github.com/ravonhenson/broc-cli/internal/state"
)

func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSelectPrefersUnseenAcrossSnapshots(t *testing.T) {
	store := openTestStore(t)

	snaps := []backend.Snapshot{{ID: "s1"}, {ID: "s2"}}
	files := map[string][]backend.FileEntry{
		"s1": {{Path: "/a", Type: "file", Size: 10}, {Path: "/b", Type: "file", Size: 10}},
		"s2": {{Path: "/c", Type: "file", Size: 10}, {Path: "/d", Type: "file", Size: 10}},
	}

	got, err := Select(snaps, files, store, 2, 0, 30*24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(got))
	}
	snapshotsSeen := map[string]bool{}
	for _, c := range got {
		if c.Reason != "new" {
			t.Errorf("expected reason=new, got %q", c.Reason)
		}
		snapshotsSeen[c.SnapshotID] = true
	}
	if len(snapshotsSeen) != 2 {
		t.Errorf("expected round-robin to touch both snapshots, got %v", snapshotsSeen)
	}
}

func TestSelectSkipsOversizedFiles(t *testing.T) {
	store := openTestStore(t)
	snaps := []backend.Snapshot{{ID: "s1"}}
	files := map[string][]backend.FileEntry{
		"s1": {{Path: "/big", Type: "file", Size: 1000}, {Path: "/small", Type: "file", Size: 10}},
	}

	got, err := Select(snaps, files, store, 5, 100, 30*24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(got) != 1 || got[0].Path != "/small" {
		t.Fatalf("expected only /small selected, got %+v", got)
	}
}

func TestSelectFallsBackToStale(t *testing.T) {
	store := openTestStore(t)
	now := time.Now()

	if err := store.Put(state.Record{
		SnapshotID:     "s1",
		Path:           "/a",
		SHA256:         "deadbeef",
		LastVerifiedAt: now.Add(-60 * 24 * time.Hour), // well past a 30-day reverify window
		LastStatus:     state.StatusOK,
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	snaps := []backend.Snapshot{{ID: "s1"}}
	files := map[string][]backend.FileEntry{
		"s1": {{Path: "/a", Type: "file", Size: 10}},
	}

	got, err := Select(snaps, files, store, 5, 0, 30*24*time.Hour, now)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(got) != 1 || got[0].Reason != "stale" {
		t.Fatalf("expected one stale candidate, got %+v", got)
	}
}

func TestSelectSkipsFreshlyVerified(t *testing.T) {
	store := openTestStore(t)
	now := time.Now()

	if err := store.Put(state.Record{
		SnapshotID:     "s1",
		Path:           "/a",
		SHA256:         "deadbeef",
		LastVerifiedAt: now.Add(-1 * time.Hour),
		LastStatus:     state.StatusOK,
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	snaps := []backend.Snapshot{{ID: "s1"}}
	files := map[string][]backend.FileEntry{
		"s1": {{Path: "/a", Type: "file", Size: 10}},
	}

	got, err := Select(snaps, files, store, 5, 0, 30*24*time.Hour, now)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no candidates for freshly-verified file, got %+v", got)
	}
}
