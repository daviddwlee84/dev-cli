package note_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/note"
)

const (
	repoID = "11111111-1111-4111-8111-111111111111"
	noteID = "22222222-2222-4222-8222-222222222222"
)

func fixedTime() time.Time {
	return time.Date(2026, time.August, 27, 10, 30, 0, 0, time.FixedZone("test", 8*60*60))
}

func store(t *testing.T, diagnostics *[]string) *note.Store {
	t.Helper()
	return note.NewStore(filepath.Join(t.TempDir(), "notes"),
		note.WithIDGenerator(func() string { return noteID }),
		note.WithClock(fixedTime),
		note.WithDiagnosticSink(func(path string, err error) {
			if diagnostics != nil {
				*diagnostics = append(*diagnostics, filepath.Base(path)+": "+err.Error())
			}
		}),
	)
}

func TestCreateWritesReadableMarkdown(t *testing.T) {
	var diagnostics []string
	s := store(t, &diagnostics)
	created, err := s.Create(context.Background(), repoID, "  dev-cli  ",
		"  Try an event subscription.  ", []string{" Idea ", "git", "IDEA", ""})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != noteID || created.RepositoryID != repoID || created.Repository != "dev-cli" {
		t.Errorf("identity = %+v", created)
	}
	if created.Body != "Try an event subscription.\n" {
		t.Errorf("body = %q", created.Body)
	}
	if want := []string{"git", "idea"}; !reflect.DeepEqual(created.Tags, want) {
		t.Errorf("tags = %v, want %v", created.Tags, want)
	}
	if created.Created.Location() != time.UTC || !created.Created.Equal(fixedTime().UTC()) {
		t.Errorf("created = %v", created.Created)
	}

	body, err := os.ReadFile(created.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.HasPrefix(text, "+++\n") || !strings.Contains(text, "\n+++\n\nTry an event") {
		t.Errorf("not readable TOML-frontmatter Markdown:\n%s", text)
	}
	if strings.Count(text, noteID) != 1 {
		t.Errorf("note ID should appear once in frontmatter:\n%s", text)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(created.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("note mode = %o, want owner-only", info.Mode().Perm())
		}
	}

	got, err := s.Get(noteID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != created.Body || got.Path != created.Path {
		t.Errorf("round trip = %+v", got)
	}
	if len(diagnostics) != 0 {
		t.Errorf("diagnostics = %v", diagnostics)
	}
}

func TestMultipleNotesSortNewestFirst(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes")
	ids := []string{
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}
	times := []time.Time{fixedTime(), fixedTime().Add(time.Minute)}
	i := 0
	s := note.NewStore(dir,
		note.WithIDGenerator(func() string { return ids[i] }),
		note.WithClock(func() time.Time { now := times[i]; i++; return now }),
	)
	first, err := s.Create(context.Background(), repoID, "dev-cli", "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(context.Background(), repoID, "dev-cli", "second", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.List(repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != second.ID || got[1].ID != first.ID {
		t.Errorf("order = %+v", got)
	}
}

func TestUpdatePreservesIdentityAndCreated(t *testing.T) {
	clock := fixedTime()
	s := note.NewStore(filepath.Join(t.TempDir(), "notes"),
		note.WithIDGenerator(func() string { return noteID }),
		note.WithClock(func() time.Time { return clock }),
	)
	created, err := s.Create(context.Background(), repoID, "dev-cli", "first", []string{"idea"})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	updated, err := s.Update(context.Background(), noteID, created.Revision(), "revised\n\nwith detail", []string{"done"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID || updated.RepositoryID != created.RepositoryID ||
		!updated.Created.Equal(created.Created) || !updated.Updated.Equal(clock.UTC()) {
		t.Errorf("immutable/stamped fields = %+v", updated)
	}
	if updated.Body != "revised\n\nwith detail\n" || !reflect.DeepEqual(updated.Tags, []string{"done"}) {
		t.Errorf("payload = %+v", updated)
	}
}

func TestDeleteRemovesOnlyOneNote(t *testing.T) {
	s := store(t, nil)
	n, err := s.Create(context.Background(), repoID, "dev-cli", "delete me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(n.ID); !errors.Is(err, note.ErrNotFound) {
		t.Errorf("Get after delete = %v", err)
	}
	if _, err := os.Stat(n.Path); !os.IsNotExist(err) {
		t.Error("file should be gone")
	}
}

func TestCreateRejectsEmptyAndInvalidRepository(t *testing.T) {
	s := store(t, nil)
	if _, err := s.Create(context.Background(), repoID, "dev-cli", "  \n", nil); !errors.Is(err, note.ErrInvalid) {
		t.Errorf("empty body = %v", err)
	}
	if _, err := s.Create(context.Background(), "../escape", "dev-cli", "thought", nil); !errors.Is(err, note.ErrRepository) {
		t.Errorf("invalid repository ID = %v", err)
	}
}

func TestListSkipsMalformedAndReportsDiagnostic(t *testing.T) {
	var diagnostics []string
	s := store(t, &diagnostics)
	if _, err := s.Create(context.Background(), repoID, "dev-cli", "valid", nil); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(s.Path(repoID), "44444444-4444-4444-8444-444444444444.md")
	if err := os.WriteFile(bad, []byte("not frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(diagnostics) != 1 || !strings.Contains(diagnostics[0], "missing opening") {
		t.Errorf("notes=%d diagnostics=%v", len(got), diagnostics)
	}
}

func TestFilenameAndRepositoryDirectoryMustMatchFrontmatter(t *testing.T) {
	s := store(t, nil)
	n, err := s.Create(context.Background(), repoID, "dev-cli", "valid", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(n.Path)
	body = []byte(strings.Replace(string(body), noteID, "55555555-5555-4555-8555-555555555555", 1))
	if err := os.WriteFile(n.Path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(noteID); err == nil || !strings.Contains(err.Error(), "does not match filename") {
		t.Errorf("mismatch = %v", err)
	}
}

func TestConcurrentCreatesDoNotLoseNotes(t *testing.T) {
	s := note.NewStore(filepath.Join(t.TempDir(), "notes"))
	const count = 20
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Create(context.Background(), repoID, "dev-cli", "thought", []string{"parallel"})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List(repoID)
	if err != nil || len(got) != count {
		t.Errorf("notes=%d err=%v", len(got), err)
	}
}

func TestPreview(t *testing.T) {
	n := note.Note{Body: "\n\nA long first line with   spaces\nsecond"}
	if got := n.Preview(18); got != "A long first line…" {
		t.Errorf("Preview = %q", got)
	}
	if got := n.Preview(0); got != "A long first line with spaces" {
		t.Errorf("unlimited Preview = %q", got)
	}
}

func TestStaleRevisionCannotOverwriteNewerUpdate(t *testing.T) {
	clock := fixedTime()
	s := note.NewStore(filepath.Join(t.TempDir(), "notes"),
		note.WithIDGenerator(func() string { return noteID }),
		note.WithClock(func() time.Time { return clock }),
	)
	original, err := s.Create(context.Background(), repoID, "dev-cli", "original", []string{"first"})
	if err != nil {
		t.Fatal(err)
	}
	staleRevision := original.Revision()
	clock = clock.Add(time.Minute)
	newer, err := s.Update(context.Background(), original.ID, staleRevision, "newer process", []string{"newer"})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	if _, err := s.Update(context.Background(), original.ID, staleRevision, "stale editor", []string{"stale"}); !errors.Is(err, note.ErrConflict) {
		t.Fatalf("stale update = %v, want ErrConflict", err)
	}
	got, _ := s.Get(original.ID)
	if got.Revision() != newer.Revision() || strings.TrimSpace(got.Body) != "newer process" || got.Tags[0] != "newer" {
		t.Errorf("newer version was overwritten: %+v", got)
	}
}
