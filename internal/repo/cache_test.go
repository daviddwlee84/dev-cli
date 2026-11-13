package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPresentationCacheRequiresMatchingScopeAndValidRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos-v1.json")
	root := t.TempDir()
	repos, state := filepath.Join(root, "repos"), filepath.Join(root, "state")
	key := SnapshotKey([]string{repos}, state)
	snapshot := Snapshot{Fingerprint: key, ObservedAt: time.Now(), Rows: []CachedRow{{Repo: Repo{Name: "demo", Path: filepath.Join(repos, "demo")}}}}
	if err := WriteSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(context.Background(), path, key); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(context.Background(), path, SnapshotKey([]string{filepath.Join(root, "other")}, state)); err == nil {
		t.Fatal("wrong scope accepted")
	}
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(context.Background(), path, key); err == nil {
		t.Fatal("malformed cache accepted")
	}
}

// Ordering hints round-trip, and a snapshot written before they existed still
// decodes with zero hints rather than forcing an uncached start.
func TestPresentationCacheCarriesOrderingHintsAndReadsOlderFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos-v1.json")
	root := t.TempDir()
	key := SnapshotKey([]string{root}, filepath.Join(root, "state"))
	row := CachedRow{Repo: Repo{Name: "demo", Path: filepath.Join(root, "demo")}, Worktrees: 2, Live: true}
	if err := WriteSnapshot(path, Snapshot{Fingerprint: key, ObservedAt: time.Now(), Rows: []CachedRow{row}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadSnapshot(context.Background(), path, key)
	if err != nil || len(snapshot.Rows) != 1 || snapshot.Rows[0].Worktrees != 2 || !snapshot.Rows[0].Live {
		t.Fatalf("hints lost: %+v, %v", snapshot.Rows, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.ReplaceAll(strings.ReplaceAll(string(data), `,"Worktrees":2`, ""), `,"Live":true`, "")
	if legacy == string(data) {
		t.Fatal("test did not strip the hint fields")
	}
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err = ReadSnapshot(context.Background(), path, key)
	if err != nil || snapshot.Rows[0].Worktrees != 0 || snapshot.Rows[0].Live {
		t.Fatalf("older cache rejected or misread: %+v, %v", snapshot.Rows, err)
	}
}
