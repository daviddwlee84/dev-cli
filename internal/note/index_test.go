package note_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/note"
)

func indexedNote(id, repositoryID, repository, body string, tags ...string) *note.Note {
	return &note.Note{
		SchemaVersion: note.CurrentSchemaVersion,
		ID:            id, RepositoryID: repositoryID, Repository: repository,
		Created: fixedTime().UTC(), Updated: fixedTime().UTC(),
		Body: body + "\n", Tags: tags,
	}
}

func index(t *testing.T) *note.Index {
	t.Helper()
	i, err := note.OpenIndex(filepath.Join(t.TempDir(), "cache", "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = i.Close() })
	return i
}

func TestIndexRebuildAndTermWisePrefixSearch(t *testing.T) {
	i := index(t)
	notes := []*note.Note{
		indexedNote(noteID, repoID, "dev-cli", "replace polling with event subscription", "idea", "backend"),
		indexedNote("33333333-3333-4333-8333-333333333333",
			"44444444-4444-4444-8444-444444444444", "web", "redesign settings page", "ui"),
	}
	if err := i.Rebuild(notes); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"event sub", "subscription polling", "idea event"} {
		hits, err := i.Search(query, "", 10)
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		if len(hits) != 1 || hits[0].ID != noteID {
			t.Errorf("Search(%q) = %+v", query, hits)
		}
		if !strings.Contains(hits[0].Snippet, "[") {
			t.Errorf("snippet should highlight match: %q", hits[0].Snippet)
		}
	}
}

func TestSearchRepositoryScopeAndUnicode(t *testing.T) {
	i := index(t)
	otherRepo := "44444444-4444-4444-8444-444444444444"
	notes := []*note.Note{
		indexedNote(noteID, repoID, "dev-cli", "記得改善 worktree 管理", "想法"),
		indexedNote("33333333-3333-4333-8333-333333333333", otherRepo, "other", "worktree docs", "idea"),
	}
	if err := i.Rebuild(notes); err != nil {
		t.Fatal(err)
	}
	hits, err := i.Search("worktree", repoID, 10)
	if err != nil || len(hits) != 1 || hits[0].RepositoryID != repoID {
		t.Errorf("scoped hits=%+v err=%v", hits, err)
	}
	hits, err = i.Search("改善", repoID, 10)
	if err != nil || len(hits) != 1 {
		t.Errorf("unicode hits=%+v err=%v", hits, err)
	}
}

func TestIndexIncrementalUpsertAndDelete(t *testing.T) {
	i := index(t)
	n := indexedNote(noteID, repoID, "dev-cli", "first wording")
	if err := i.Upsert(n); err != nil {
		t.Fatal(err)
	}
	if hits, _ := i.Search("first", "", 10); len(hits) != 1 {
		t.Fatalf("upsert not searchable: %+v", hits)
	}

	n.Body = "revised wording\n"
	n.Updated = n.Updated.Add(time.Minute)
	if err := i.Upsert(n); err != nil {
		t.Fatal(err)
	}
	if hits, _ := i.Search("first", "", 10); len(hits) != 0 {
		t.Errorf("old body still indexed: %+v", hits)
	}
	if hits, _ := i.Search("revised", "", 10); len(hits) != 1 {
		t.Errorf("new body not indexed: %+v", hits)
	}

	if err := i.Delete(n.ID); err != nil {
		t.Fatal(err)
	}
	if hits, _ := i.Search("revised", "", 10); len(hits) != 0 {
		t.Errorf("deleted note still indexed: %+v", hits)
	}
}

func TestEnsureRebuildsOnlyWhenManifestDiffers(t *testing.T) {
	i := index(t)
	n := indexedNote(noteID, repoID, "dev-cli", "original")
	if i.Current([]*note.Note{n}) {
		t.Error("empty index should not match one source note")
	}
	if err := i.Ensure([]*note.Note{n}); err != nil {
		t.Fatal(err)
	}
	if !i.Current([]*note.Note{n}) {
		t.Error("index should be current after Ensure")
	}
	// Direct Markdown edits may leave frontmatter Updated unchanged. Content
	// fingerprinting still makes the index stale.
	n.Body = "manually edited\n"
	if i.Current([]*note.Note{n}) {
		t.Error("body change without timestamp must make index stale")
	}
	if err := i.Ensure([]*note.Note{n}); err != nil {
		t.Fatal(err)
	}
	n.Updated = n.Updated.Add(time.Second)
	n.Body = "changed\n"
	if i.Current([]*note.Note{n}) {
		t.Error("changed source timestamp should make the index stale")
	}
	if err := i.Ensure([]*note.Note{n}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := i.Search("changed", "", 10); len(hits) != 1 {
		t.Errorf("stale index was not rebuilt: %+v", hits)
	}
}

func TestSearchLiteralPunctuationDoesNotBecomeFTSSyntax(t *testing.T) {
	i := index(t)
	n := indexedNote(noteID, repoID, "dev-cli", "try foo-bar and C++ later")
	if err := i.Rebuild([]*note.Note{n}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"foo-bar", `"quoted`, "C++", "", "   "} {
		if _, err := i.Search(query, "", 10); err != nil {
			t.Errorf("Search(%q) treated input as syntax: %v", query, err)
		}
	}
}

func TestIndexDatabaseIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "notes.db")
	i, err := note.OpenIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = i.Close()
	// SQLite creates with the process umask; OpenIndex should tighten it because
	// note bodies may contain private thoughts.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("notes index mode = %o, want private", info.Mode().Perm())
	}
}
