package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ravonhenson/broc-cli/internal/verify"
)

func TestExitCodeMatrix(t *testing.T) {
	cases := []struct {
		name   string
		result verify.RunResult
		runErr error
		want   int
	}{
		{"clean run", verify.RunResult{}, nil, ExitOK},
		{"mismatch", verify.RunResult{Mismatches: 1}, nil, ExitFindings},
		{"per-file error", verify.RunResult{Errors: 1}, nil, ExitFindings},
		{"operational error wins over clean result", verify.RunResult{}, errors.New("boom"), ExitError},
		{"operational error wins over findings", verify.RunResult{Mismatches: 1}, errors.New("boom"), ExitError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExitCode(c.result, c.runErr); got != c.want {
				t.Errorf("ExitCode() = %d, want %d", got, c.want)
			}
		})
	}
}

func TestPrintTextRunFailed(t *testing.T) {
	var buf bytes.Buffer
	PrintText(&buf, verify.RunResult{RepoName: "myrepo"}, errors.New("backend unreachable"))
	out := buf.String()
	if !strings.Contains(out, "RUN FAILED") || !strings.Contains(out, "backend unreachable") {
		t.Errorf("PrintText output = %q, want it to mention the run failure", out)
	}
}

func TestPrintTextSummarizesFindings(t *testing.T) {
	var buf bytes.Buffer
	result := verify.RunResult{
		RepoName:   "myrepo",
		StartedAt:  time.Now(),
		FinishedAt: time.Now().Add(2 * time.Second),
		Files: []verify.FileResult{
			{SnapshotID: "abcdef1234567890", Path: "/ok.txt", Status: "ok"},
			{SnapshotID: "abcdef1234567890", Path: "/new.txt", Status: "baselined", SHA256: "abc123"},
			{SnapshotID: "abcdef1234567890", Path: "/bad.txt", Status: "mismatch", Error: "content hash changed"},
		},
		NewBaselines: 1,
		Mismatches:   1,
	}
	PrintText(&buf, result, nil)
	out := buf.String()
	for _, want := range []string{"/ok.txt", "/new.txt", "/bad.txt", "MISMATCH", "FAILED"} {
		if !strings.Contains(out, want) {
			t.Errorf("PrintText output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintJSONShape(t *testing.T) {
	var buf bytes.Buffer
	result := verify.RunResult{
		RepoName: "myrepo",
		Files:    []verify.FileResult{{SnapshotID: "s1", Path: "/a", Status: "ok"}},
	}
	if err := PrintJSON(&buf, result, nil); err != nil {
		t.Fatalf("PrintJSON: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if decoded["repo"] != "myrepo" {
		t.Errorf("repo = %v, want myrepo", decoded["repo"])
	}
	if decoded["ok"] != true {
		t.Errorf("ok = %v, want true", decoded["ok"])
	}
	if _, hasErr := decoded["error"]; hasErr {
		t.Errorf("error key should be omitted on success, got %v", decoded["error"])
	}
}

func TestPrintJSONIncludesErrorOnFailure(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintJSON(&buf, verify.RunResult{RepoName: "myrepo"}, errors.New("kaboom")); err != nil {
		t.Fatalf("PrintJSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if decoded["ok"] != false {
		t.Errorf("ok = %v, want false", decoded["ok"])
	}
	if decoded["error"] != "kaboom" {
		t.Errorf("error = %v, want kaboom", decoded["error"])
	}
}
