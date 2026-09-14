package restic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ravonhenson/drillbit/internal/backend"
)

// These tests drive the real restic binary against a real local repository.
// They validate the assumptions restic_test.go's fake scripts encode -
// that our parsing actually matches what restic really emits - and catch
// drift across restic versions. They're skipped (not failed) when restic
// isn't installed, so `go test ./...` still works on a machine without it;
// CI installs restic specifically so these always run there.
func requireRestic(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic not found on PATH; skipping integration test")
	}
	return path
}

// testRepo creates a fresh local restic repository and returns a Backend
// wired to it, plus the source directory used to seed it.
func testRepo(t *testing.T) (*Backend, string) {
	t.Helper()
	requireRestic(t)

	repoDir := filepath.Join(t.TempDir(), "repo")
	srcDir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	b := New(Config{Repository: repoDir, Env: map[string]string{"RESTIC_PASSWORD": "test-password-123"}})

	initCmd := exec.Command("restic", "init")
	initCmd.Env = append(os.Environ(), "RESTIC_REPOSITORY="+repoDir, "RESTIC_PASSWORD=test-password-123")
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("restic init: %v\n%s", err, out)
	}
	return b, srcDir
}

func resticBackup(t *testing.T, repoDir, srcDir string) {
	t.Helper()
	cmd := exec.Command("restic", "backup", srcDir)
	cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY="+repoDir, "RESTIC_PASSWORD=test-password-123")
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

func TestIntegration_ListSnapshotsMatchesRealRepo(t *testing.T) {
	b, src := testRepo(t)
	repoDir := b.cfg.Repository

	writeFile(t, filepath.Join(src, "a.txt"), []byte("hello"))
	resticBackup(t, repoDir, src)
	writeFile(t, filepath.Join(src, "b.txt"), []byte("world"))
	resticBackup(t, repoDir, src)

	snaps, err := b.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snaps))
	}
	if snaps[0].Time.After(snaps[1].Time) {
		t.Errorf("snapshots not in chronological order: %v then %v", snaps[0].Time, snaps[1].Time)
	}
	for _, s := range snaps {
		if s.ID == "" || len(s.Paths) == 0 {
			t.Errorf("snapshot missing expected fields: %+v", s)
		}
	}
}

func TestIntegration_ListFilesFindsRealFiles(t *testing.T) {
	b, src := testRepo(t)
	repoDir := b.cfg.Repository

	writeFile(t, filepath.Join(src, "top.txt"), []byte("top level"))
	writeFile(t, filepath.Join(src, "nested", "deep", "file.txt"), []byte("deeply nested"))
	writeFile(t, filepath.Join(src, "empty.txt"), nil)
	writeFile(t, filepath.Join(src, "unicode-é-名前.txt"), []byte("unicode name"))
	writeFile(t, filepath.Join(src, "with spaces.txt"), []byte("has spaces"))
	resticBackup(t, repoDir, src)

	snaps, err := b.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("ListSnapshots: snaps=%v err=%v", snaps, err)
	}

	files, err := b.ListFiles(context.Background(), snaps[0].ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}

	byBase := map[string]backend.FileEntry{}
	for _, f := range files {
		byBase[filepath.Base(f.Path)] = f
	}

	wantNames := []string{"top.txt", "file.txt", "empty.txt", "unicode-é-名前.txt", "with spaces.txt"}
	for _, name := range wantNames {
		f, ok := byBase[name]
		if !ok {
			t.Errorf("expected to find a file named %q among %d files, got: %+v", name, len(files), byBase)
			continue
		}
		if f.Type != "file" {
			t.Errorf("%q has type %q, want file", name, f.Type)
		}
	}
	if f, ok := byBase["empty.txt"]; ok && f.Size != 0 {
		t.Errorf("empty.txt size = %d, want 0", f.Size)
	}
}

func TestIntegration_RestoreFileRoundTripsContentExactly(t *testing.T) {
	b, src := testRepo(t)
	repoDir := b.cfg.Repository

	content := []byte("the quick brown fox jumps over the lazy dog\x00\x01\x02binary-ish bytes too")
	writeFile(t, filepath.Join(src, "sub", "target.bin"), content)
	resticBackup(t, repoDir, src)

	snaps, err := b.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("ListSnapshots: snaps=%v err=%v", snaps, err)
	}
	files, err := b.ListFiles(context.Background(), snaps[0].ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	var target backend.FileEntry
	for _, f := range files {
		if filepath.Base(f.Path) == "target.bin" {
			target = f
		}
	}
	if target.Path == "" {
		t.Fatalf("target.bin not found in %+v", files)
	}

	dest := t.TempDir()
	res, err := b.RestoreFile(context.Background(), snaps[0].ID, target.Path, dest)
	if err != nil {
		t.Fatalf("RestoreFile: %v", err)
	}
	got, err := os.ReadFile(res.LocalPath)
	if err != nil {
		t.Fatalf("reading restored file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("restored content mismatch: got %q, want %q", got, content)
	}
	if res.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", res.Size, len(content))
	}
}

func TestIntegration_SameFileDifferentContentAcrossSnapshotsYieldsDifferentHashes(t *testing.T) {
	b, src := testRepo(t)
	repoDir := b.cfg.Repository

	path := filepath.Join(src, "changing.txt")
	writeFile(t, path, []byte("version one"))
	resticBackup(t, repoDir, src)
	writeFile(t, path, []byte("version two, different length even"))
	resticBackup(t, repoDir, src)

	snaps, err := b.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 2 {
		t.Fatalf("ListSnapshots: snaps=%v err=%v", snaps, err)
	}

	hashOf := func(snapID string) string {
		files, err := b.ListFiles(context.Background(), snapID)
		if err != nil {
			t.Fatalf("ListFiles(%s): %v", snapID, err)
		}
		var rel string
		for _, f := range files {
			if filepath.Base(f.Path) == "changing.txt" {
				rel = f.Path
			}
		}
		if rel == "" {
			t.Fatalf("changing.txt not found in snapshot %s", snapID)
		}
		res, err := b.RestoreFile(context.Background(), snapID, rel, t.TempDir())
		if err != nil {
			t.Fatalf("RestoreFile(%s): %v", snapID, err)
		}
		data, err := os.ReadFile(res.LocalPath)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}

	h1 := hashOf(snaps[0].ID)
	h2 := hashOf(snaps[1].ID)
	if h1 == h2 {
		t.Fatal("expected different content hashes for the two snapshot versions of changing.txt")
	}
}

func TestIntegration_WrongPasswordFailsCleanly(t *testing.T) {
	b, src := testRepo(t)
	repoDir := b.cfg.Repository
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hello"))
	resticBackup(t, repoDir, src)

	wrong := New(Config{Repository: repoDir, Env: map[string]string{"RESTIC_PASSWORD": "definitely-wrong"}})
	_, err := wrong.ListSnapshots(context.Background())
	if err == nil {
		t.Fatal("expected an error with the wrong repository password")
	}
}

func TestIntegration_NonexistentRepoFailsCleanly(t *testing.T) {
	requireRestic(t)
	b := New(Config{Repository: filepath.Join(t.TempDir(), "does-not-exist"), Env: map[string]string{"RESTIC_PASSWORD": "x"}})
	_, err := b.ListSnapshots(context.Background())
	if err == nil {
		t.Fatal("expected an error for a repository that was never initialized")
	}
}

// TestIntegration_IncludeMatchingNothingIsReportedAsError exercises the
// real restic behavior behind RestoreFile's "reported success but file is
// missing" guard: restic exits 0 and restores 0 files when --include
// matches nothing, rather than erroring - drillbit must catch that itself.
func TestIntegration_IncludeMatchingNothingIsReportedAsError(t *testing.T) {
	b, src := testRepo(t)
	repoDir := b.cfg.Repository
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hello"))
	resticBackup(t, repoDir, src)

	snaps, err := b.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("ListSnapshots: snaps=%v err=%v", snaps, err)
	}

	_, err = b.RestoreFile(context.Background(), snaps[0].ID, "/this/path/does/not/exist.txt", t.TempDir())
	if err == nil {
		t.Fatal("expected RestoreFile to error out when --include matches nothing")
	}
}

// TestIntegration_CorruptedPackFailsRestoreRatherThanSilentlyReturningBadData
// documents and locks in the key real-world behavior driving drillbit's
// design: restic content-addresses and checksums blobs, so corrupting a
// pack file on disk makes RestoreFile *error*, not silently succeed with
// different bytes. The mismatch-detection path in verify.Run is defense in
// depth; this is the failure mode that actually happens in practice.
func TestIntegration_CorruptedPackFailsRestoreRatherThanSilentlyReturningBadData(t *testing.T) {
	b, src := testRepo(t)
	repoDir := b.cfg.Repository

	content := []byte(strings.Repeat("integrity-check-payload-", 200)) // large enough to land in its own pack reliably
	writeFile(t, filepath.Join(src, "important.bin"), content)
	resticBackup(t, repoDir, src)

	snaps, err := b.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("ListSnapshots: snaps=%v err=%v", snaps, err)
	}
	files, err := b.ListFiles(context.Background(), snaps[0].ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	var target string
	for _, f := range files {
		if filepath.Base(f.Path) == "important.bin" {
			target = f.Path
		}
	}
	if target == "" {
		t.Fatalf("important.bin not found: %+v", files)
	}

	// Sanity: a clean restore works before we corrupt anything.
	if _, err := b.RestoreFile(context.Background(), snaps[0].ID, target, t.TempDir()); err != nil {
		t.Fatalf("baseline restore before corruption failed: %v", err)
	}

	corruptAllPackFiles(t, repoDir)

	_, err = b.RestoreFile(context.Background(), snaps[0].ID, target, t.TempDir())
	if err == nil {
		t.Fatal("expected RestoreFile to fail against a corrupted repository, not silently succeed")
	}
	t.Logf("corrupted-repo restore correctly failed with: %v", err)
}

// corruptAllPackFiles flips a byte roughly in the middle of every pack file
// under <repo>/data, which restic's own blob-hash verification should
// detect on restore.
func corruptAllPackFiles(t *testing.T, repoDir string) {
	t.Helper()
	dataDir := filepath.Join(repoDir, "data")
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("reading repo data dir: %v", err)
	}
	found := false
	for _, sub := range entries {
		if !sub.IsDir() {
			continue
		}
		packs, err := os.ReadDir(filepath.Join(dataDir, sub.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, pack := range packs {
			path := filepath.Join(dataDir, sub.Name(), pack.Name())
			data, err := os.ReadFile(path)
			if err != nil || len(data) == 0 {
				continue
			}
			// Flip bytes throughout the file, not just one in the middle:
			// a pack file packs many blobs plus a trailing encrypted
			// header, and a single flipped byte has decent odds of
			// landing somewhere that doesn't affect our specific target
			// blob's authenticated decryption. Corrupting broadly makes
			// the test's intent (any restore from this pack must fail)
			// robust to pack layout.
			for i := 0; i < len(data); i += 16 {
				data[i] ^= 0xFF
			}
			// restic writes pack files read-only; make it writable first.
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no pack files found to corrupt - test fixture assumption is wrong")
	}
}

// Sanity check that our JSON parsing assumptions in restic_test.go's fake
// scripts actually match a real restic's output shape for `ls --json`.
func TestIntegration_LsJSONUsesStructTypeDiscriminator(t *testing.T) {
	b, src := testRepo(t)
	repoDir := b.cfg.Repository
	writeFile(t, filepath.Join(src, "a.txt"), []byte("hello"))
	resticBackup(t, repoDir, src)

	snaps, err := b.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("ListSnapshots: snaps=%v err=%v", snaps, err)
	}

	cmd := exec.Command("restic", "ls", snaps[0].ID, "--json")
	cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY="+repoDir, "RESTIC_PASSWORD=test-password-123")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("restic ls: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 NDJSON lines, got %d", len(lines))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line not JSON: %v", err)
	}
	if _, ok := first["struct_type"]; !ok {
		t.Errorf("first ls line has no struct_type field (restic's schema may have changed): %s", lines[0])
	}
	if _, ok := first["message_type"]; ok {
		t.Log("note: restic ls now also emits message_type - our discriminator choice (struct_type) still works, but worth knowing schema evolved")
	}
}
