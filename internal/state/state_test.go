package state

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestGetMissing(t *testing.T) {
	s, _ := openTest(t)
	_, ok, err := s.Get("snap1", "/a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for a never-written record")
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	s, _ := openTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := Record{
		SnapshotID:      "snap1",
		Path:            "/a/b.txt",
		SHA256:          "deadbeef",
		Size:            123,
		FirstVerifiedAt: now,
		LastVerifiedAt:  now,
		LastStatus:      StatusOK,
		VerifyCount:     3,
	}
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.Get("snap1", "/a/b.txt")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.SHA256 != rec.SHA256 || got.VerifyCount != 3 || !got.LastVerifiedAt.Equal(now) {
		t.Errorf("Get() = %+v, want %+v", got, rec)
	}
}

func TestPutOverwrites(t *testing.T) {
	s, _ := openTest(t)
	if err := s.Put(Record{SnapshotID: "s1", Path: "/a", SHA256: "v1", VerifyCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Record{SnapshotID: "s1", Path: "/a", SHA256: "v2", VerifyCount: 2}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("s1", "/a")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.SHA256 != "v2" || got.VerifyCount != 2 {
		t.Errorf("Get() = %+v, want the second write to win", got)
	}
}

func TestKeysDoNotCollideAcrossSnapshotsOrPaths(t *testing.T) {
	s, _ := openTest(t)
	// A naive concatenation of snapshotID+path without a separator could
	// collide, e.g. snapshot "ab"+"c" vs snapshot "a"+"bc".
	if err := s.Put(Record{SnapshotID: "ab", Path: "c", SHA256: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Record{SnapshotID: "a", Path: "bc", SHA256: "two"}); err != nil {
		t.Fatal(err)
	}
	r1, ok1, _ := s.Get("ab", "c")
	r2, ok2, _ := s.Get("a", "bc")
	if !ok1 || !ok2 {
		t.Fatalf("expected both records to exist independently: ok1=%v ok2=%v", ok1, ok2)
	}
	if r1.SHA256 != "one" || r2.SHA256 != "two" {
		t.Errorf("records collided: r1=%+v r2=%+v", r1, r2)
	}
}

func TestAllForSnapshot(t *testing.T) {
	s, _ := openTest(t)
	if err := s.Put(Record{SnapshotID: "s1", Path: "/a", SHA256: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Record{SnapshotID: "s1", Path: "/b", SHA256: "2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Record{SnapshotID: "s2", Path: "/a", SHA256: "3"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.AllForSnapshot("s1")
	if err != nil {
		t.Fatalf("AllForSnapshot: %v", err)
	}
	if len(got) != 2 || got["/a"].SHA256 != "1" || got["/b"].SHA256 != "2" {
		t.Errorf("AllForSnapshot(s1) = %+v, want 2 entries for /a and /b", got)
	}
}

func TestAll(t *testing.T) {
	s, _ := openTest(t)
	for i := 0; i < 5; i++ {
		if err := s.Put(Record{SnapshotID: "s1", Path: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("All() returned %d records, want 5", len(got))
	}
}

func TestRecentRunsOrderingAndLimit(t *testing.T) {
	s, _ := openTest(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		start := base.Add(time.Duration(i) * time.Hour)
		if err := s.PutRun(RunSummary{
			ID:        start.Format(time.RFC3339Nano),
			StartedAt: start,
			Ok:        true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.RecentRuns(3)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("RecentRuns(3) returned %d, want 3", len(got))
	}
	// Newest first.
	for i := 0; i < len(got)-1; i++ {
		if !got[i].StartedAt.After(got[i+1].StartedAt) {
			t.Errorf("RecentRuns not newest-first: %v before %v", got[i].StartedAt, got[i+1].StartedAt)
		}
	}
	if !got[0].StartedAt.Equal(base.Add(4 * time.Hour)) {
		t.Errorf("newest run = %v, want the last one written", got[0].StartedAt)
	}
}

func TestOpenCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
}

func TestOpenSecondInstanceTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits on bbolt's file-lock timeout; skipped in -short")
	}
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open (first): %v", err)
	}
	defer first.Close()

	// A second concurrent Open against the same file should fail (not hang
	// forever, not silently corrupt the db) once bbolt's lock timeout
	// elapses - this is what protects against two overlapping `drillbit
	// run` invocations against the same repo.
	_, err = Open(path)
	if err == nil {
		t.Fatal("expected the second Open to fail while the first is still held")
	}
}
