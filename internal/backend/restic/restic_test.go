package restic

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeBinary writes a shell script standing in for the real restic binary
// and returns its path. These tests exercise our argument-building and
// --json parsing in isolation, without needing the real restic binary
// installed - see restic_integration_test.go for tests against the real
// thing.
func fakeBinary(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake restic scripts are POSIX shell; not supported on windows")
	}
	path := filepath.Join(t.TempDir(), "fake-restic")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("writing fake binary: %v", err)
	}
	return path
}

func TestListSnapshotsParsesArray(t *testing.T) {
	bin := fakeBinary(t, `
cat <<'EOF'
[
  {"time":"2024-01-01T00:00:00.000000000-00:00","id":"aaaa1111","short_id":"aaaa","paths":["/data"],"tags":["daily"]},
  {"time":"2024-01-02T00:00:00.000000000-00:00","id":"bbbb2222","short_id":"bbbb","paths":["/data"],"tags":[]}
]
EOF
`)
	b := New(Config{Binary: bin})
	snaps, err := b.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snaps))
	}
	if snaps[0].ID != "aaaa1111" || snaps[0].Short != "aaaa" {
		t.Errorf("snaps[0] = %+v", snaps[0])
	}
	if snaps[0].Time.Year() != 2024 {
		t.Errorf("snaps[0].Time = %v, want a parsed 2024 timestamp", snaps[0].Time)
	}
	if len(snaps[0].Tags) != 1 || snaps[0].Tags[0] != "daily" {
		t.Errorf("snaps[0].Tags = %v, want [daily]", snaps[0].Tags)
	}
	if snaps[1].ID != "bbbb2222" {
		t.Errorf("snaps[1] = %+v", snaps[1])
	}
}

func TestListSnapshotsSurfacesStderrOnFailure(t *testing.T) {
	bin := fakeBinary(t, `
echo "Fatal: wrong password" >&2
exit 1
`)
	b := New(Config{Binary: bin})
	_, err := b.ListSnapshots(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "wrong password") {
		t.Errorf("error = %v, want it to include restic's stderr", err)
	}
}

func TestListSnapshotsRejectsMalformedJSON(t *testing.T) {
	bin := fakeBinary(t, `echo 'not json'`)
	b := New(Config{Binary: bin})
	_, err := b.ListSnapshots(context.Background())
	if err == nil {
		t.Fatal("expected a parse error for malformed JSON")
	}
}

func TestListFilesCollectsOnlyFileNodes(t *testing.T) {
	bin := fakeBinary(t, `
cat <<'EOF'
{"struct_type":"snapshot","id":"aaaa1111","time":"2024-01-01T00:00:00Z"}
{"struct_type":"node","name":"dir1","type":"dir","path":"/dir1","size":0,"mtime":"2024-01-01T00:00:00Z"}
{"struct_type":"node","name":"file1.txt","type":"file","path":"/dir1/file1.txt","size":42,"mtime":"2024-01-01T00:00:00Z"}
{"struct_type":"node","name":"link1","type":"symlink","path":"/link1","size":0,"mtime":"2024-01-01T00:00:00Z"}
{"struct_type":"node","name":"file2.txt","type":"file","path":"/file2.txt","size":7,"mtime":"2024-01-02T00:00:00Z"}
EOF
`)
	b := New(Config{Binary: bin})
	files, err := b.ListFiles(context.Background(), "aaaa1111")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2 (dirs/symlinks excluded): %+v", len(files), files)
	}
	if files[0].Path != "/dir1/file1.txt" || files[0].Size != 42 {
		t.Errorf("files[0] = %+v", files[0])
	}
	if files[1].Path != "/file2.txt" || files[1].Size != 7 {
		t.Errorf("files[1] = %+v", files[1])
	}
}

func TestListFilesToleratesStrayNonJSONLines(t *testing.T) {
	bin := fakeBinary(t, `
cat <<'EOF'
warning: something unrelated printed to stdout
{"struct_type":"node","name":"file1.txt","type":"file","path":"/file1.txt","size":1,"mtime":"2024-01-01T00:00:00Z"}
EOF
`)
	b := New(Config{Binary: bin})
	files, err := b.ListFiles(context.Background(), "aaaa1111")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "/file1.txt" {
		t.Fatalf("got %+v, want just /file1.txt to survive the stray line", files)
	}
}

func TestListFilesSurfacesStderrOnFailure(t *testing.T) {
	bin := fakeBinary(t, `
echo "Fatal: no such snapshot" >&2
exit 1
`)
	b := New(Config{Binary: bin})
	_, err := b.ListFiles(context.Background(), "bogus")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no such snapshot") {
		t.Errorf("error = %v, want it to include restic's stderr", err)
	}
}

// restoreScript builds a fake restic that, given `restore <snap> --target
// <dir> --include <path> --json`, writes `content` to <dir><path> and
// prints a successful summary line.
const restoreScriptSuccess = `
target=""
include=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--target" ]; then target="$arg"; fi
  if [ "$prev" = "--include" ]; then include="$arg"; fi
  prev="$arg"
done
mkdir -p "$(dirname "$target$include")"
printf 'restored-content' > "$target$include"
echo '{"message_type":"summary","files_restored":1}'
`

func TestRestoreFileWritesAndHashesContent(t *testing.T) {
	bin := fakeBinary(t, restoreScriptSuccess)
	b := New(Config{Binary: bin})
	dest := t.TempDir()

	res, err := b.RestoreFile(context.Background(), "snap1", "/a/b.txt", dest)
	if err != nil {
		t.Fatalf("RestoreFile: %v", err)
	}
	want := filepath.Join(dest, "/a/b.txt")
	if res.LocalPath != want {
		t.Errorf("LocalPath = %q, want %q", res.LocalPath, want)
	}
	data, err := os.ReadFile(res.LocalPath)
	if err != nil {
		t.Fatalf("reading restored file: %v", err)
	}
	if string(data) != "restored-content" {
		t.Errorf("restored content = %q, want %q", data, "restored-content")
	}
	if res.Size != int64(len("restored-content")) {
		t.Errorf("Size = %d, want %d", res.Size, len("restored-content"))
	}
}

func TestRestoreFileSurfacesRestoreErrorLine(t *testing.T) {
	bin := fakeBinary(t, `
echo '{"message_type":"error","item":"/a/b.txt","error":{"message":"blob not found"}}'
echo '{"message_type":"summary","files_restored":0}'
`)
	b := New(Config{Binary: bin})
	_, err := b.RestoreFile(context.Background(), "snap1", "/a/b.txt", t.TempDir())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "blob not found") {
		t.Errorf("error = %v, want it to include restic's reported error message", err)
	}
}

func TestRestoreFileDetectsMissingFileDespiteZeroExit(t *testing.T) {
	// Simulates `--include` matching nothing: restic exits 0 with no error
	// lines, but never actually creates the target file.
	bin := fakeBinary(t, `echo '{"message_type":"summary","files_restored":0}'`)
	b := New(Config{Binary: bin})
	_, err := b.RestoreFile(context.Background(), "snap1", "/does/not/exist.txt", t.TempDir())
	if err == nil {
		t.Fatal("expected an error when restic reports success but never wrote the file")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error = %v, want it to call out the missing file", err)
	}
}

func TestRestoreFileSurfacesNonZeroExit(t *testing.T) {
	bin := fakeBinary(t, `
echo "Fatal: repository is locked" >&2
exit 1
`)
	b := New(Config{Binary: bin})
	_, err := b.RestoreFile(context.Background(), "snap1", "/a.txt", t.TempDir())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error = %v, want it to include restic's stderr", err)
	}
}

func TestCommandPropagatesEnv(t *testing.T) {
	bin := fakeBinary(t, `
echo "{\"repo\":\"$RESTIC_REPOSITORY\",\"pwcmd\":\"$RESTIC_PASSWORD_COMMAND\",\"custom\":\"$MY_CUSTOM_VAR\"}"
`)
	b := New(Config{
		Binary:          bin,
		Repository:      "/tmp/myrepo",
		PasswordCommand: "echo hunter2",
		Env:             map[string]string{"MY_CUSTOM_VAR": "hello"},
	})
	out, err := b.runJSON(context.Background(), "envtest")
	if err != nil {
		t.Fatalf("runJSON: %v", err)
	}
	got := string(out)
	for _, want := range []string{`"repo":"/tmp/myrepo"`, `"pwcmd":"echo hunter2"`, `"custom":"hello"`} {
		if !strings.Contains(got, want) {
			t.Errorf("env output = %s, want it to contain %s", got, want)
		}
	}
}

func TestCommandAppendsExtraArgs(t *testing.T) {
	bin := fakeBinary(t, `echo "$@"`)
	b := New(Config{Binary: bin, ExtraArgs: []string{"--limit-download", "5000"}})
	out, err := b.runJSON(context.Background(), "snapshots", "--json")
	if err != nil {
		t.Fatalf("runJSON: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "--limit-download 5000") {
		t.Errorf("args = %q, want extra args appended after the base args", got)
	}
}
