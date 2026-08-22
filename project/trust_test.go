package project

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTrustStorePersistsAndInheritsDecisions(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "nested", "project")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config", "trust.json")
	store, err := OpenTrustStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Decision(child); got != TrustUnknown {
		t.Fatalf("initial decision = %q, want unknown", got)
	}
	if err := store.Set(root, TrustAlways); err != nil {
		t.Fatal(err)
	}
	if got := store.Decision(child); got != TrustAlways {
		t.Fatalf("inherited decision = %q, want always", got)
	}
	if err := store.Set(child, TrustNever); err != nil {
		t.Fatal(err)
	}

	reloaded, err := OpenTrustStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Decision(child); got != TrustNever {
		t.Fatalf("reloaded decision = %q, want never", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("trust store permissions = %o, want 600", perm)
		}
	}
}

func TestTrustStoreRejectsInvalidDecision(t *testing.T) {
	store, err := OpenTrustStore(filepath.Join(t.TempDir(), "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(t.TempDir(), TrustUnknown); err == nil {
		t.Fatal("Set() accepted TrustUnknown")
	}
}

func TestTrustStoreRollsBackDecisionWhenSaveFails(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &TrustStore{
		path:      filepath.Join(blocker, "trust.json"),
		decisions: map[string]TrustDecision{},
	}
	projectPath := t.TempDir()
	if err := store.Set(projectPath, TrustAlways); err == nil {
		t.Fatal("Set() unexpectedly succeeded")
	}
	if got := store.Decision(projectPath); got != TrustUnknown {
		t.Fatalf("decision after failed save = %q, want unknown", got)
	}
}

func TestOpenTrustStoreRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTrustStore(path); err == nil {
		t.Fatal("OpenTrustStore() accepted malformed JSON")
	}
}
