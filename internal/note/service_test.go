package note_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/note"

	_ "modernc.org/sqlite"
)

func TestServiceMutationKeepsIndexInSync(t *testing.T) {
	root := t.TempDir()
	store := note.NewStore(filepath.Join(root, "notes"),
		note.WithIDGenerator(func() string { return noteID }), note.WithClock(fixedTime))
	service := note.NewService(store, filepath.Join(root, "cache", "notes.db"))

	n, err := service.Add(context.Background(), repoID, "dev-cli", "first thought", []string{"idea"})
	if err != nil {
		t.Fatal(err)
	}
	if hits, err := service.Search("first", repoID, 10); err != nil || len(hits) != 1 {
		t.Fatalf("add index hits=%+v err=%v", hits, err)
	}
	n, err = service.Update(context.Background(), n.ID, n.Revision(), "revised thought", []string{"done"})
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := service.Search("first", repoID, 10); len(hits) != 0 {
		t.Errorf("stale body remains: %+v", hits)
	}
	if hits, _ := service.Search("revised", repoID, 10); len(hits) != 1 || hits[0].ID != n.ID {
		t.Errorf("updated body missing: %+v", hits)
	}
	if err := service.Delete(context.Background(), n.ID); err != nil {
		t.Fatal(err)
	}
	if hits, _ := service.Search("revised", repoID, 10); len(hits) != 0 {
		t.Errorf("deleted note remains: %+v", hits)
	}
}

func TestClearIndexPreservesSourceAndSearchRebuilds(t *testing.T) {
	root := t.TempDir()
	store := note.NewStore(filepath.Join(root, "notes"),
		note.WithIDGenerator(func() string { return noteID }), note.WithClock(fixedTime))
	service := note.NewService(store, filepath.Join(root, "cache", "notes.db"))
	n, err := service.Add(context.Background(), repoID, "dev-cli", "survives cache clear", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ClearIndex(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.IndexPath); !os.IsNotExist(err) {
		t.Error("index should be gone")
	}
	if _, err := os.Stat(n.Path); err != nil {
		t.Error("durable Markdown must remain")
	}
	hits, err := service.Search("survives", repoID, 10)
	if err != nil || len(hits) != 1 {
		t.Errorf("search should rebuild: hits=%+v err=%v", hits, err)
	}
}

func TestReindexReportsCount(t *testing.T) {
	root := t.TempDir()
	ids := []string{noteID, "33333333-3333-4333-8333-333333333333"}
	i := 0
	store := note.NewStore(filepath.Join(root, "notes"), note.WithIDGenerator(func() string {
		id := ids[i]
		i++
		return id
	}))
	service := note.NewService(store, filepath.Join(root, "notes.db"))
	for _, body := range []string{"one", "two"} {
		if _, err := service.Add(context.Background(), repoID, "dev-cli", body, nil); err != nil {
			t.Fatal(err)
		}
	}
	_ = service.ClearIndex()
	count, err := service.Reindex()
	if err != nil || count != 2 {
		t.Errorf("Reindex = %d, %v", count, err)
	}
}

func TestSearchRecoversCorruptIndexFromMarkdown(t *testing.T) {
	root := t.TempDir()
	store := note.NewStore(filepath.Join(root, "notes"),
		note.WithIDGenerator(func() string { return noteID }), note.WithClock(fixedTime))
	service := note.NewService(store, filepath.Join(root, "cache", "notes.db"))
	n, err := service.Add(context.Background(), repoID, "dev-cli", "survives corruption", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Close happened after incremental update; replace only the disposable DB.
	if err := os.WriteFile(service.IndexPath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	hits, err := service.Search("corruption", repoID, 10)
	if err != nil || len(hits) != 1 || hits[0].ID != n.ID {
		t.Errorf("recovered hits=%+v err=%v", hits, err)
	}
}

func TestSearchRecoversValidSQLiteWithIncompatibleSchema(t *testing.T) {
	root := t.TempDir()
	store := note.NewStore(filepath.Join(root, "notes"),
		note.WithIDGenerator(func() string { return noteID }), note.WithClock(fixedTime))
	indexPath := filepath.Join(root, "cache", "notes.db")
	service := note.NewService(store, indexPath)
	n, err := service.Add(context.Background(), repoID, "dev-cli", "schema survives", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ClearIndex(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE notes_fts (id TEXT, repository_id TEXT, repository TEXT, tags TEXT, body TEXT);
CREATE TABLE note_manifest (id TEXT PRIMARY KEY, updated TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	hits, err := service.Search("schema", repoID, 10)
	if err != nil || len(hits) != 1 || hits[0].ID != n.ID {
		t.Errorf("recovered hits=%+v err=%v", hits, err)
	}
}
