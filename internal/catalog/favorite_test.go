package catalog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
)

func TestFavoriteDefaultsFalseAndSurvivesCatalogUpdates(t *testing.T) {
	root := t.TempDir()
	store := catalog.NewStore(filepath.Join(root, "assets"))
	entry := sampleEntry(root)
	if err := store.Create(entry); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(store.Dir, entry.ID+".toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "favorite") {
		t.Fatal("default false should preserve the existing TOML shape")
	}
	loaded, err := store.Get(entry.ID)
	if err != nil || loaded.Favorite {
		t.Fatalf("legacy entry default = %+v, %v", loaded, err)
	}
	registry := catalog.NewRegistry(store)
	for _, desired := range []bool{true, false} {
		updated, err := registry.Patch(entry.ID, func(e *catalog.Entry) error {
			e.Favorite = desired
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := catalog.NewStore(store.Dir).Get(entry.ID)
		if err != nil || reopened.Favorite != desired || updated.Clone().Favorite != desired {
			t.Fatalf("favorite %v did not round-trip: %+v, %v", desired, reopened, err)
		}
		if reopened.Note != loaded.Note || !reopened.HasTag("go") {
			t.Fatal("favorite update lost unrelated metadata")
		}
	}
}
