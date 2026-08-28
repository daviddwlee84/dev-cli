package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/note"
)

func TestPrepareTUINoteEditUpdatesAtomicallyAfterEditor(t *testing.T) {
	root := t.TempDir()
	service := note.NewService(note.NewStore(filepath.Join(root, "notes")), filepath.Join(root, "cache", "notes.db"))
	n, err := service.Add(context.Background(), "11111111-1111-4111-8111-111111111111", "demo", "original", []string{"idea"})
	if err != nil {
		t.Fatal(err)
	}
	editor := filepath.Join(root, "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'changed in TUI\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", editor)
	app := &App{Notes: service}
	edit, err := prepareTUINoteEdit(app, n)
	if err != nil {
		t.Fatal(err)
	}
	runErr := edit.Command.Run()
	if err := edit.Complete(runErr); err != nil {
		t.Fatal(err)
	}
	got, err := service.Get(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got.Body) != "changed in TUI" || len(got.Tags) != 1 || got.Tags[0] != "idea" {
		t.Errorf("updated note = %+v", got)
	}
	if hits, _ := service.Search("changed", n.RepositoryID, 10); len(hits) != 1 {
		t.Errorf("editor completion should reindex: %+v", hits)
	}
}

func TestPrepareTUINoteEditFailureDoesNotChangeSource(t *testing.T) {
	root := t.TempDir()
	service := note.NewService(note.NewStore(filepath.Join(root, "notes")), filepath.Join(root, "cache", "notes.db"))
	n, err := service.Add(context.Background(), "11111111-1111-4111-8111-111111111111", "demo", "original", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "false")
	app := &App{Notes: service}
	edit, err := prepareTUINoteEdit(app, n)
	if err != nil {
		t.Fatal(err)
	}
	runErr := edit.Command.Run()
	if runErr == nil {
		t.Fatal("fixture editor should fail")
	}
	if err := edit.Complete(runErr); err == nil {
		t.Error("completion should preserve editor failure")
	}
	got, _ := service.Get(n.ID)
	if strings.TrimSpace(got.Body) != "original" {
		t.Errorf("failed editor changed source: %q", got.Body)
	}
}

func TestPrepareTUINoteEditConflictPreservesTemporaryBody(t *testing.T) {
	root := t.TempDir()
	service := note.NewService(note.NewStore(filepath.Join(root, "notes")), filepath.Join(root, "cache", "notes.db"))
	n, err := service.Add(context.Background(), "11111111-1111-4111-8111-111111111111", "demo", "original", nil)
	if err != nil {
		t.Fatal(err)
	}
	editor := filepath.Join(root, "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'stale edited body\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", editor)
	app := &App{Notes: service}
	edit, err := prepareTUINoteEdit(app, n)
	if err != nil {
		t.Fatal(err)
	}
	// Another process wins after the editor snapshot was taken.
	if _, err := service.Update(context.Background(), n.ID, n.Revision(), "newer body", nil); err != nil {
		t.Fatal(err)
	}
	runErr := edit.Command.Run()
	err = edit.Complete(runErr)
	if !errors.Is(err, note.ErrConflict) || !strings.Contains(err.Error(), "preserved at") {
		t.Fatalf("completion = %v", err)
	}
	parts := strings.Split(err.Error(), "preserved at ")
	if len(parts) != 2 {
		t.Fatalf("recovery path missing: %v", err)
	}
	recovery := strings.TrimSpace(parts[1])
	body, statErr := os.ReadFile(recovery)
	if statErr != nil || strings.TrimSpace(string(body)) != "stale edited body" {
		t.Errorf("recovery body=%q err=%v path=%s", body, statErr, recovery)
	}
	_ = os.Remove(recovery)
	current, _ := service.Get(n.ID)
	if strings.TrimSpace(current.Body) != "newer body" {
		t.Errorf("newer body was lost: %q", current.Body)
	}
}
