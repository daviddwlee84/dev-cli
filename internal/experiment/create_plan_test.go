package experiment_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/experiment"
)

func TestCreatePlanPreservesPreviewDestinationAndRejectsCollision(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	p, err := f.service.PlanCreate(t.Context(), experiment.CreateRequest{Name: "review", NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(p.Path); !os.IsNotExist(err) {
		t.Fatal("preview created directory")
	}
	if err = os.Mkdir(p.Path, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.ApplyCreate(t.Context(), p); err == nil {
		t.Fatal("occupied destination accepted")
	}
	if _, err = os.Stat(p.Path + "-2"); !os.IsNotExist(err) {
		t.Fatal("apply silently changed destination")
	}
}

func TestCreatePlanRejectsChangedTargetAndEnrollsOnlySelectedTry(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	unrelated := f.mkdir("unrelated")
	p, err := f.service.PlanCreate(t.Context(), experiment.CreateRequest{Name: "review", NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	changed := p
	changed.Path = filepath.Join(f.tries, "redirect")
	if _, err = f.service.ApplyCreate(t.Context(), changed); err == nil {
		t.Fatal("changed plan accepted")
	}
	created, err := f.service.ApplyCreate(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := f.store.List()
	if err != nil || len(entries) != 1 || entries[0].ID != created.Item.ID {
		t.Fatalf("catalog=%+v %v", entries, err)
	}
	if entries[0].Locations["test-host"].CurrentPath == unrelated {
		t.Fatal("unrelated Try enrolled")
	}
}
