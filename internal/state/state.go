// Package state persists broc's verification ledger: which
// (snapshot, file) pairs have been restored and diffed before, what their
// baseline content hash was, and when they were last checked. This is what
// lets coverage accumulate incrementally across many scheduled runs instead
// of requiring one expensive full scan.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketVerifications = []byte("verifications")
	bucketRuns          = []byte("runs")
)

// Status is the outcome of a single file verification.
type Status string

const (
	StatusOK        Status = "ok"        // restored and matched the stored baseline
	StatusMismatch  Status = "mismatch"  // restored, but content differs from baseline
	StatusError     Status = "error"     // restore itself failed
	StatusBaselined Status = "baselined" // first time this pair was seen; hash recorded as baseline
)

// Record is the ledger entry for one (snapshot, path) pair.
type Record struct {
	SnapshotID      string    `json:"snapshot_id"`
	Path            string    `json:"path"`
	SHA256          string    `json:"sha256"`
	Size            int64     `json:"size"`
	FirstVerifiedAt time.Time `json:"first_verified_at"`
	LastVerifiedAt  time.Time `json:"last_verified_at"`
	LastStatus      Status    `json:"last_status"`
	LastError       string    `json:"last_error,omitempty"`
	VerifyCount     int       `json:"verify_count"`
}

func key(snapshotID, path string) []byte {
	return []byte(snapshotID + "\x00" + path)
}

// RunSummary records the outcome of one `broc run` invocation, for
// history/status reporting.
type RunSummary struct {
	ID           string    `json:"id"` // RFC3339 start time, also the bolt key
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	FilesChecked int       `json:"files_checked"`
	NewBaselines int       `json:"new_baselines"`
	Mismatches   int       `json:"mismatches"`
	Errors       int       `json:"errors"`
	Ok           bool      `json:"ok"`
}

// Store wraps a bbolt database holding one repository's ledger.
type Store struct {
	db *bolt.DB
}

// Open opens (creating if needed) the state database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("opening state db %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketVerifications); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketRuns); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Get returns the ledger entry for (snapshotID, path), or ok=false if it has
// never been verified.
func (s *Store) Get(snapshotID, path string) (rec Record, ok bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketVerifications).Get(key(snapshotID, path))
		if v == nil {
			return nil
		}
		ok = true
		return json.Unmarshal(v, &rec)
	})
	return rec, ok, err
}

// Put writes (overwrites) the ledger entry for (snapshotID, path).
func (s *Store) Put(rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketVerifications).Put(key(rec.SnapshotID, rec.Path), data)
	})
}

// AllForSnapshot returns every ledger entry recorded for a given snapshot,
// keyed by path.
func (s *Store) AllForSnapshot(snapshotID string) (map[string]Record, error) {
	out := map[string]Record{}
	prefix := []byte(snapshotID + "\x00")
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketVerifications).Cursor()
		for k, v := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, v = c.Next() {
			var rec Record
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			out[rec.Path] = rec
		}
		return nil
	})
	return out, err
}

// All returns every ledger entry in the store.
func (s *Store) All() ([]Record, error) {
	var out []Record
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketVerifications).ForEach(func(_, v []byte) error {
			var rec Record
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			out = append(out, rec)
			return nil
		})
	})
	return out, err
}

// PutRun records a completed run's summary.
func (s *Store) PutRun(r RunSummary) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRuns).Put([]byte(r.ID), data)
	})
}

// RecentRuns returns up to n most recent run summaries, newest first.
func (s *Store) RecentRuns(n int) ([]RunSummary, error) {
	var out []RunSummary
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketRuns).Cursor()
		count := 0
		for k, v := c.Last(); k != nil && count < n; k, v = c.Prev() {
			var r RunSummary
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			out = append(out, r)
			count++
		}
		return nil
	})
	return out, err
}

func hasPrefix(b, prefix []byte) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i := range prefix {
		if b[i] != prefix[i] {
			return false
		}
	}
	return true
}
