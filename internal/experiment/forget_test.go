package experiment_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
)

func forgetFixture(t *testing.T) (*fixture, experiment.Item) {
	t.Helper()
	hooks := removalHooks()
	hooks.ForgetGuard = func(context.Context, *catalog.Entry) (string, error) { return "unreferenced", nil }
	f := newFixture(t, hooks, 1)
	item := removalItem(t, f)
	if err := os.RemoveAll(item.Live.CurrentPath); err != nil {
		t.Fatal(err)
	}
	return f, item
}

func TestForgetMissingEntryHasNoFilesystemEffect(t *testing.T) {
	f, item := forgetFixture(t)
	plan, err := f.service.PlanForget(t.Context(), item.ID, item.Entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Get(item.ID); err != nil {
		t.Fatal("preview removed record", err)
	}
	result, err := f.service.ApplyForget(t.Context(), plan)
	if err != nil || result.Outcome != "forgotten" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := f.store.Get(item.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(item.Live.CurrentPath); !os.IsNotExist(err) {
		t.Fatal("created missing path", err)
	}
	if _, err := os.Stat(result.Journal); err != nil {
		t.Fatal("missing audit", err)
	}
	// Reusing the name gives the new experiment a new identity. The old audit
	// must not become a permanent ownership claim over that directory name.
	replacement := item.Entry.Clone()
	replacement.ID = ""
	if err := f.store.Create(replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PlanForget(t.Context(), replacement.ID, nil); err != nil {
		t.Fatal("completed audit claimed a new Try", err)
	}
}

func TestForgetFailsClosedForChangedAuthority(t *testing.T) {
	for _, change := range []string{"reappeared", "catalog", "other-host", "root-missing", "root-replaced", "symlink", "notes", "external-git"} {
		t.Run(change, func(t *testing.T) {
			f, item := forgetFixture(t)
			plan, err := f.service.PlanForget(t.Context(), item.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "reappeared":
				err = os.Mkdir(item.Live.CurrentPath, 0700)
			case "catalog":
				_, err = f.store.Update(item.ID, func(e *catalog.Entry) error { e.Name = "changed"; return nil })
			case "other-host":
				_, err = f.store.Update(item.ID, func(e *catalog.Entry) error {
					l, _ := e.LocationFor("test-host")
					e.Locations["another-host"] = l
					return nil
				})
			case "root-missing":
				err = os.Rename(f.tries, f.tries+"-away")
			case "root-replaced":
				if err = os.Rename(f.tries, f.tries+"-away"); err == nil {
					err = os.Mkdir(f.tries, 0700)
				}
			case "symlink":
				err = os.Symlink(t.TempDir(), item.Live.CurrentPath)
			case "notes":
				_, err = f.store.Update(item.ID, func(e *catalog.Entry) error { e.Note = "important"; return nil })
			case "external-git":
				_, err = f.store.Update(item.ID, func(e *catalog.Entry) error {
					l, _ := e.LocationFor("test-host")
					l.GitCommonDir = t.TempDir()
					e.Locations["test-host"] = l
					return nil
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.ApplyForget(t.Context(), plan); err == nil {
				t.Fatal("stale authority accepted")
			}
			if _, err := f.store.Get(item.ID); err != nil {
				t.Fatal("record removed", err)
			}
			if change == "root-missing" || change == "symlink" {
				if got := f.service.Presence(item.Live.CurrentPath); got != "unavailable" {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestForgetOtherHostAndRecoveryBlockPlanning(t *testing.T) {
	f, item := forgetFixture(t)
	_, err := f.store.Update(item.ID, func(e *catalog.Entry) error {
		l, _ := e.LocationFor("test-host")
		e.Locations["another-host"] = l
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PlanForget(t.Context(), item.ID, nil); err == nil {
		t.Fatal("other host ignored")
	}
}

func TestTrashRejectsNewCatalogClaimAfterPreview(t *testing.T) {
	hooks := removalHooks()
	calls := 0
	hooks.Trash = func(context.Context, string) error { calls++; return nil }
	f := newFixture(t, hooks, 1)
	item := removalItem(t, f)
	plan, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	other := item.Entry.Clone()
	other.ID = ""
	other.Name = "another claim"
	if err := f.store.Create(other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyRemoval(t.Context(), plan); err == nil || calls != 0 {
		t.Fatalf("ambiguous removal applied: calls=%d err=%v", calls, err)
	}
}

func TestReadOnlyTryListIncludesMissingWithoutEnrollment(t *testing.T) {
	f, item := forgetFixture(t)
	f.mkdir("unregistered")
	items, _, err := f.service.List(t.Context(), experiment.ListOptions{ReadOnly: true, IncludeMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("%+v", items)
	}
	for _, i := range items {
		if i.ID == item.ID && i.Live.Presence != "missing" {
			t.Fatal(i.Live)
		}
	}
	entries, err := f.store.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("read enrolled metadata: %v %v", entries, err)
	}
	items, _, err = f.service.List(t.Context(), experiment.ListOptions{ReadOnly: true, IncludeMissing: true, Paths: []string{item.Live.CurrentPath}})
	if err != nil || len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("scope leaked: %+v %v", items, err)
	}
}

func TestUncatalogedTrashEnrollsOnlyAtApplyAndPreservesContents(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{true: "failure", false: "success"}[fail], func(t *testing.T) {
			hooks := removalHooks()
			trash := filepath.Join(t.TempDir(), "trash")
			hooks.Trash = func(_ context.Context, path string) error {
				if fail {
					return errors.New("trash failed")
				}
				return os.Rename(path, trash)
			}
			f := newFixture(t, hooks, 1)
			path := f.mkdir("selected")
			f.mkdir("unrelated")
			if err := os.WriteFile(filepath.Join(path, ".private"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			plan, err := f.service.PlanRemoval(t.Context(), experiment.RemovalRequest{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Register {
				t.Fatal("enrollment omitted")
			}
			entries, _ := f.store.List()
			if len(entries) != 0 {
				t.Fatal("preview enrolled")
			}
			result, err := f.service.ApplyRemoval(t.Context(), plan)
			if (err != nil) != fail {
				t.Fatalf("%+v %v", result, err)
			}
			entries, err = f.store.List()
			if err != nil || len(entries) != 1 {
				t.Fatalf("wrong enrollment: %+v %v", entries, err)
			}
			source := trash
			if fail {
				source = path
			}
			data, err := os.ReadFile(filepath.Join(source, ".private"))
			if err != nil || string(data) != "keep" {
				t.Fatalf("lost contents: %q %v", data, err)
			}
		})
	}
}
