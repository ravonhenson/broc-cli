// Package verify orchestrates one drill: sample candidates, restore each
// into scratch space, hash it, and diff that hash against the baseline
// recorded the first time broc ever saw that exact (snapshot, path)
// pair. Snapshots are immutable, so a baseline that stops matching means
// the backend silently returned different bytes than it did before -
// exactly the kind of corruption `restic check` (which validates pack
// integrity, not a full restore round-trip) can miss.
package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/ravonhenson/broc-cli/internal/backend"
	"github.com/ravonhenson/broc-cli/internal/config"
	"github.com/ravonhenson/broc-cli/internal/sampler"
	"github.com/ravonhenson/broc-cli/internal/state"
)

// FileResult is the outcome of verifying a single (snapshot, path) pair.
type FileResult struct {
	SnapshotID string       `json:"snapshot_id"`
	Path       string       `json:"path"`
	Size       int64        `json:"size"`
	SHA256     string       `json:"sha256"`
	Status     state.Status `json:"status"`
	Reason     string       `json:"reason"` // "new" or "stale", from the sampler
	Error      string       `json:"error,omitempty"`
}

// RunResult summarizes one `broc run` invocation.
type RunResult struct {
	RepoName     string       `json:"repo_name"`
	StartedAt    time.Time    `json:"started_at"`
	FinishedAt   time.Time    `json:"finished_at"`
	Files        []FileResult `json:"files"`
	NewBaselines int          `json:"new_baselines"`
	Mismatches   int          `json:"mismatches"`
	Errors       int          `json:"errors"`
}

// OK reports whether the run found no data-integrity problems. It does not
// account for operational errors reaching this point (e.g. the backend
// being unreachable) - callers should treat those separately.
func (r RunResult) OK() bool {
	return r.Mismatches == 0 && r.Errors == 0
}

// MaxSnapshotsPerRun caps how many snapshots' file listings broc will
// enumerate in one run, so a repository with thousands of snapshots stays
// fast per-run. The newest snapshot is always included; the rest are
// randomly sampled, so coverage still spreads across history over many
// runs. 0 disables the cap.
const defaultMaxSnapshotsPerRun = 25

// Run executes one drill against be, using store as the coverage ledger.
func Run(ctx context.Context, cfg *config.Config, be backend.Backend, store *state.Store) (RunResult, error) {
	result := RunResult{RepoName: cfg.Name, StartedAt: time.Now().UTC()}

	allSnapshots, err := be.ListSnapshots(ctx)
	if err != nil {
		return result, fmt.Errorf("listing snapshots: %w", err)
	}
	if len(allSnapshots) == 0 {
		result.FinishedAt = time.Now().UTC()
		return result, nil
	}

	snapshots := chooseSnapshots(allSnapshots, defaultMaxSnapshotsPerRun)

	filesBySnapshot := make(map[string][]backend.FileEntry, len(snapshots))
	for _, snap := range snapshots {
		files, err := be.ListFiles(ctx, snap.ID)
		if err != nil {
			return result, fmt.Errorf("listing files in snapshot %s: %w", snap.Short, err)
		}
		filesBySnapshot[snap.ID] = files
	}

	candidates, err := sampler.Select(
		snapshots, filesBySnapshot, store,
		cfg.Sample.FilesPerRun, cfg.MaxFileSize(), cfg.ReverifyAfter(), time.Now(),
	)
	if err != nil {
		return result, fmt.Errorf("selecting sample: %w", err)
	}

	runScratch := filepath.Join(cfg.Scratch.Dir, result.StartedAt.Format("20060102T150405Z"))
	if err := os.MkdirAll(runScratch, 0o755); err != nil {
		return result, fmt.Errorf("creating scratch dir: %w", err)
	}
	if !cfg.Scratch.Keep {
		defer os.RemoveAll(runScratch)
	}

	for i, c := range candidates {
		fr := verifyOne(ctx, be, store, runScratch, i, c)
		result.Files = append(result.Files, fr)
		switch fr.Status {
		case state.StatusBaselined:
			result.NewBaselines++
		case state.StatusMismatch:
			result.Mismatches++
		case state.StatusError:
			result.Errors++
		}
	}

	result.FinishedAt = time.Now().UTC()

	summary := state.RunSummary{
		ID:           result.StartedAt.Format(time.RFC3339Nano),
		StartedAt:    result.StartedAt,
		FinishedAt:   result.FinishedAt,
		FilesChecked: len(result.Files),
		NewBaselines: result.NewBaselines,
		Mismatches:   result.Mismatches,
		Errors:       result.Errors,
		Ok:           result.OK(),
	}
	if err := store.PutRun(summary); err != nil {
		return result, fmt.Errorf("recording run summary: %w", err)
	}

	return result, nil
}

func verifyOne(ctx context.Context, be backend.Backend, store *state.Store, runScratch string, idx int, c sampler.Candidate) FileResult {
	fr := FileResult{SnapshotID: c.SnapshotID, Path: c.Path, Reason: c.Reason}

	destDir := filepath.Join(runScratch, fmt.Sprintf("%d", idx))
	restored, err := be.RestoreFile(ctx, c.SnapshotID, c.Path, destDir)
	if err != nil {
		fr.Status = state.StatusError
		fr.Error = err.Error()
		recordError(store, c, err)
		return fr
	}

	sum, err := hashFile(restored.LocalPath)
	if err != nil {
		fr.Status = state.StatusError
		fr.Error = fmt.Sprintf("hashing restored file: %s", err)
		recordError(store, c, err)
		return fr
	}
	fr.Size = restored.Size
	fr.SHA256 = sum

	now := time.Now().UTC()
	existing, ok, _ := store.Get(c.SnapshotID, c.Path)
	rec := state.Record{
		SnapshotID:     c.SnapshotID,
		Path:           c.Path,
		Size:           restored.Size,
		LastVerifiedAt: now,
	}
	switch {
	case !ok:
		rec.SHA256 = sum
		rec.FirstVerifiedAt = now
		rec.VerifyCount = 1
		rec.LastStatus = state.StatusBaselined
		fr.Status = state.StatusBaselined
	case existing.SHA256 == sum:
		rec.SHA256 = sum
		rec.FirstVerifiedAt = existing.FirstVerifiedAt
		rec.VerifyCount = existing.VerifyCount + 1
		rec.LastStatus = state.StatusOK
		fr.Status = state.StatusOK
	default:
		// Baseline is kept as-is (not overwritten) so a corruption event
		// keeps being reported on every run until a human investigates and
		// explicitly clears it, rather than silently "healing" itself.
		rec.SHA256 = existing.SHA256
		rec.FirstVerifiedAt = existing.FirstVerifiedAt
		rec.VerifyCount = existing.VerifyCount + 1
		rec.LastStatus = state.StatusMismatch
		rec.LastError = fmt.Sprintf("content hash changed: baseline %s, got %s", existing.SHA256, sum)
		fr.Status = state.StatusMismatch
		fr.Error = rec.LastError
	}
	if err := store.Put(rec); err != nil {
		fr.Status = state.StatusError
		fr.Error = fmt.Sprintf("recording result: %s", err)
	}
	return fr
}

func recordError(store *state.Store, c sampler.Candidate, cause error) {
	now := time.Now().UTC()
	existing, ok, _ := store.Get(c.SnapshotID, c.Path)
	rec := state.Record{
		SnapshotID:     c.SnapshotID,
		Path:           c.Path,
		LastVerifiedAt: now,
		LastStatus:     state.StatusError,
		LastError:      cause.Error(),
	}
	if ok {
		rec.SHA256 = existing.SHA256
		rec.Size = existing.Size
		rec.FirstVerifiedAt = existing.FirstVerifiedAt
		rec.VerifyCount = existing.VerifyCount + 1
	} else {
		rec.FirstVerifiedAt = now
		rec.VerifyCount = 1
	}
	_ = store.Put(rec) // best-effort; the run result already carries the error
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// chooseSnapshots bounds how many snapshots get their files listed this
// run: always the newest, plus a random sample of the rest up to max.
func chooseSnapshots(all []backend.Snapshot, max int) []backend.Snapshot {
	if max <= 0 || len(all) <= max {
		return all
	}
	newest := all[len(all)-1]
	rest := make([]backend.Snapshot, len(all)-1)
	copy(rest, all[:len(all)-1])
	rand.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	chosen := append([]backend.Snapshot{newest}, rest[:max-1]...)
	return chosen
}
