// Package report formats a drill's outcome for humans and machines, and
// picks the exit code that makes failures impossible to script around.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ravonhenson/broc-cli/internal/verify"
)

// Exit codes. Distinguishing 1 (data problem found) from 2 (broc
// couldn't complete its job) matters for alerting: a mismatch means your
// backups may not be restorable; an operational error just means this run
// didn't get to check.
const (
	ExitOK       = 0
	ExitFindings = 1
	ExitError    = 2
)

// ExitCode picks the process exit code for a run.
func ExitCode(result verify.RunResult, runErr error) int {
	switch {
	case runErr != nil:
		return ExitError
	case !result.OK():
		return ExitFindings
	default:
		return ExitOK
	}
}

// jsonReport is the machine-readable shape printed by --json.
type jsonReport struct {
	Repo  string           `json:"repo"`
	OK    bool             `json:"ok"`
	Error string           `json:"error,omitempty"`
	Run   verify.RunResult `json:"run"`
}

// PrintJSON writes the structured result to w.
func PrintJSON(w io.Writer, result verify.RunResult, runErr error) error {
	rep := jsonReport{
		Repo: result.RepoName,
		OK:   runErr == nil && result.OK(),
		Run:  result,
	}
	if runErr != nil {
		rep.Error = runErr.Error()
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// PrintText writes a human-readable summary to w.
func PrintText(w io.Writer, result verify.RunResult, runErr error) {
	if runErr != nil {
		fmt.Fprintf(w, "broc: %s: RUN FAILED: %v\n", result.RepoName, runErr)
		return
	}

	for _, f := range result.Files {
		switch f.Status {
		case "ok":
			fmt.Fprintf(w, "  ok       %s @ %.8s\n", f.Path, f.SnapshotID)
		case "baselined":
			fmt.Fprintf(w, "  baseline %s @ %.8s (first check, sha256=%.12s)\n", f.Path, f.SnapshotID, f.SHA256)
		case "mismatch":
			fmt.Fprintf(w, "  MISMATCH %s @ %.8s: %s\n", f.Path, f.SnapshotID, f.Error)
		case "error":
			fmt.Fprintf(w, "  ERROR    %s @ %.8s: %s\n", f.Path, f.SnapshotID, f.Error)
		}
	}

	status := "OK"
	if !result.OK() {
		status = "FAILED"
	}
	fmt.Fprintf(w, "\nbroc: %s: %s - %d checked, %d new baselines, %d mismatches, %d errors (%s)\n",
		result.RepoName, status, len(result.Files), result.NewBaselines, result.Mismatches, result.Errors,
		result.FinishedAt.Sub(result.StartedAt).Round(1e6))
}
