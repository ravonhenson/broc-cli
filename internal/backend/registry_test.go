package backend

import (
	"context"
	"strings"
	"testing"
)

type stubBackend struct{ name string }

func (s stubBackend) Name() string                                          { return s.name }
func (s stubBackend) ListSnapshots(ctx context.Context) ([]Snapshot, error) { return nil, nil }
func (s stubBackend) ListFiles(ctx context.Context, id string) ([]FileEntry, error) {
	return nil, nil
}
func (s stubBackend) RestoreFile(ctx context.Context, id, path, dest string) (RestoreResult, error) {
	return RestoreResult{}, nil
}

func TestRegisterAndNew(t *testing.T) {
	Register("stub-for-test", func(cfg map[string]any) (Backend, error) {
		return stubBackend{name: "stub-for-test"}, nil
	})

	b, err := New("stub-for-test", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b.Name() != "stub-for-test" {
		t.Errorf("Name() = %q, want stub-for-test", b.Name())
	}
}

func TestNewUnknownBackend(t *testing.T) {
	_, err := New("does-not-exist", nil)
	if err == nil {
		t.Fatal("expected an error for an unregistered backend")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error = %v, want it to name the unknown backend", err)
	}
}

func TestNamesIncludesRegistered(t *testing.T) {
	Register("another-stub", func(cfg map[string]any) (Backend, error) {
		return stubBackend{name: "another-stub"}, nil
	})
	names := Names()
	found := false
	for _, n := range names {
		if n == "another-stub" {
			found = true
		}
	}
	if !found {
		t.Errorf("Names() = %v, want it to include another-stub", names)
	}
}
