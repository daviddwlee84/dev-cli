package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPresentationCacheRequiresMatchingScopeAndValidRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos-v1.json")
	key := SnapshotKey([]string{"/repos"}, "/state")
	snapshot := Snapshot{Fingerprint: key, ObservedAt: time.Now(), Rows: []CachedRow{{Repo: Repo{Name: "demo", Path: "/repos/demo"}}}}
	if err := WriteSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(context.Background(), path, key); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(context.Background(), path, SnapshotKey([]string{"/other"}, "/state")); err == nil {
		t.Fatal("wrong scope accepted")
	}
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(context.Background(), path, key); err == nil {
		t.Fatal("malformed cache accepted")
	}
}
