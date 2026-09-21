// Package sampler decides which (snapshot, file) pairs to verify on a given
// run, so that coverage accumulates across the whole repository over many
// runs instead of requiring one expensive full scan.
package sampler

import (
	"math/rand/v2"
	"sort"
	"time"

	"github.com/ravonhenson/broc-cli/internal/backend"
	"github.com/ravonhenson/broc-cli/internal/state"
)

// Candidate is one file selected for verification this run.
type Candidate struct {
	SnapshotID string
	Path       string
	Size       int64
	// Reason explains why this candidate was picked: "new" (never verified
	// before) or "stale" (due for re-verification, to catch bit rot).
	Reason string
}

// Select picks up to n candidates for this run.
//
// Priority:
//  1. Pairs never verified before ("new"), spread round-robin across
//     snapshots so early runs sample breadth (many snapshots a little)
//     rather than exhausting one snapshot before touching the next.
//  2. Once every known pair has been verified at least once, pairs whose
//     last verification is older than reverifyAfter ("stale"), oldest
//     first, so long-term bit rot / silent corruption still gets caught.
func Select(
	snapshots []backend.Snapshot,
	filesBySnapshot map[string][]backend.FileEntry,
	store *state.Store,
	n int,
	maxSize int64,
	reverifyAfter time.Duration,
	now time.Time,
) ([]Candidate, error) {
	if n <= 0 {
		return nil, nil
	}

	// Bucket unseen files by snapshot, in original per-snapshot order.
	unseenBySnapshot := make(map[string][]Candidate, len(snapshots))
	var staleCandidates []Candidate
	var staleTimes []time.Time

	for _, snap := range snapshots {
		files := filesBySnapshot[snap.ID]
		for _, f := range files {
			if f.Type != "file" {
				continue
			}
			if maxSize > 0 && f.Size > maxSize {
				continue
			}
			rec, ok, err := store.Get(snap.ID, f.Path)
			if err != nil {
				return nil, err
			}
			if !ok {
				unseenBySnapshot[snap.ID] = append(unseenBySnapshot[snap.ID], Candidate{
					SnapshotID: snap.ID,
					Path:       f.Path,
					Size:       f.Size,
					Reason:     "new",
				})
				continue
			}
			if now.Sub(rec.LastVerifiedAt) >= reverifyAfter {
				staleCandidates = append(staleCandidates, Candidate{
					SnapshotID: snap.ID,
					Path:       f.Path,
					Size:       f.Size,
					Reason:     "stale",
				})
				staleTimes = append(staleTimes, rec.LastVerifiedAt)
			}
		}
	}

	// Shuffle within each snapshot's unseen list so repeated runs don't
	// always pick the same first N files in directory-listing order.
	snapIDs := make([]string, 0, len(unseenBySnapshot))
	for id, list := range unseenBySnapshot {
		rand.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
		unseenBySnapshot[id] = list
		snapIDs = append(snapIDs, id)
	}
	rand.Shuffle(len(snapIDs), func(i, j int) { snapIDs[i], snapIDs[j] = snapIDs[j], snapIDs[i] })

	var out []Candidate

	// Round-robin across snapshots for breadth.
	for len(out) < n {
		took := false
		for _, id := range snapIDs {
			list := unseenBySnapshot[id]
			if len(list) == 0 {
				continue
			}
			out = append(out, list[0])
			unseenBySnapshot[id] = list[1:]
			took = true
			if len(out) >= n {
				break
			}
		}
		if !took {
			break // exhausted every snapshot's unseen files
		}
	}

	// Fill any remaining budget with the oldest stale entries.
	if len(out) < n && len(staleCandidates) > 0 {
		sort.Sort(byStaleTime{staleCandidates, staleTimes})
		remaining := n - len(out)
		if remaining > len(staleCandidates) {
			remaining = len(staleCandidates)
		}
		out = append(out, staleCandidates[:remaining]...)
	}

	return out, nil
}

type byStaleTime struct {
	c []Candidate
	t []time.Time
}

func (b byStaleTime) Len() int           { return len(b.c) }
func (b byStaleTime) Less(i, j int) bool { return b.t[i].Before(b.t[j]) }
func (b byStaleTime) Swap(i, j int) {
	b.c[i], b.c[j] = b.c[j], b.c[i]
	b.t[i], b.t[j] = b.t[j], b.t[i]
}
