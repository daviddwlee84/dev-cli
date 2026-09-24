package experiment_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
)

func TestSuggestedGraduateNameUsesSuccessfulHistoryAndPortableLegacyPaths(t *testing.T) {
	graduatedAt := time.Date(2026, time.September, 24, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, remembered, legacy, want string
		graduated                      bool
	}{
		{name: "current Try", want: "prototype"},
		{name: "remembered beats destination", remembered: "chosen-project", legacy: "/projects/old-directory", graduated: true, want: "chosen-project"},
		{name: "legacy POSIX", legacy: "/projects/Tools/parser-core", graduated: true, want: "parser-core"},
		{name: "legacy Windows", legacy: `C:\projects\Tools\parser-core`, graduated: true, want: "parser-core"},
		{name: "legacy UNC", legacy: `\\server\projects\parser-core`, graduated: true, want: "parser-core"},
		{name: "unsuccessful history ignored", legacy: "/projects/not-completed", want: "prototype"},
		{name: "legacy root ignored", legacy: "/", graduated: true, want: "prototype"},
		{name: "legacy volume ignored", legacy: `C:\`, graduated: true, want: "prototype"},
		{name: "unnormalized basename ignored", legacy: "/projects/ parser-core ", graduated: true, want: "prototype"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := &catalog.Experiment{GraduatedName: tc.remembered, GraduatedPath: tc.legacy}
			if tc.graduated {
				history.GraduatedAt = graduatedAt
			}
			item := experiment.Item{
				Basename: "2026-09-20-prototype",
				Entry:    &catalog.Entry{Experiment: history},
			}
			if got := experiment.SuggestedGraduateName(item); got != tc.want {
				t.Fatalf("suggested name = %q, want %q", got, tc.want)
			}
			if history.GraduatedName != tc.remembered || history.GraduatedPath != tc.legacy {
				t.Fatal("suggesting a name modified graduation history")
			}
		})
	}
}

func TestGraduateNamePlanPrecedenceLeavesCatalogBytesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, remembered, legacy, explicit, want string
	}{
		{name: "explicit", remembered: "remembered", legacy: "/projects/legacy", explicit: "override", want: "override"},
		{name: "remembered", remembered: "remembered", legacy: "/projects/legacy", want: "remembered"},
		{name: "legacy POSIX", legacy: "/projects/Tools/legacy-name", want: "legacy-name"},
		{name: "legacy Windows", legacy: `C:\projects\Tools\legacy-name`, want: "legacy-name"},
		{name: "current", want: "prototype"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, experiment.Hooks{}, 1)
			source := f.mkdir("2026-09-20-prototype")
			items, _, err := f.service.List(t.Context(), experiment.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			item := itemByBase(t, items, filepath.Base(source))
			if tc.legacy != "" {
				if _, err := f.registry.Update(item.ID, func(entry *catalog.Entry) error {
					entry.Experiment.GraduatedAt = f.now.Add(-time.Hour)
					entry.Experiment.GraduatedPath = tc.legacy
					entry.Experiment.GraduatedName = tc.remembered
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(f.store.Dir, item.ID+".toml")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := f.service.PlanGraduate(t.Context(), experiment.GraduateRequest{
				Ref: item.ID, Name: tc.explicit, DryRun: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Name != tc.want || filepath.Base(plan.Destination) != tc.want || plan.Category != "" {
				t.Fatalf("wrong planned naming: %+v", plan)
			}
			if result, err := f.service.ApplyGraduate(t.Context(), plan); err != nil || result.Moved {
				t.Fatalf("dry-run applied a move: %+v, %v", result, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("name planning rewrote catalog bytes: %v", err)
			}
			if tc.remembered == "" && bytes.Contains(after, []byte("graduated_name")) {
				t.Fatal("legacy/default name was persisted by a read or dry-run")
			}
		})
	}
}

func TestGraduateNameSurvivesDemoteWithoutRememberingCategory(t *testing.T) {
	f := newFixture(t, demoteHooks(), 1)
	source := f.mkdir("2026-09-20-prototype")
	initRepo(t, source, true)
	items, _, err := f.service.List(t.Context(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, filepath.Base(source))
	first, err := f.service.Graduate(t.Context(), experiment.GraduateRequest{
		Ref: item.ID, Name: "chosen-project", Category: "Tools",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Item.Entry.Experiment.GraduatedName != "chosen-project" {
		t.Fatalf("successful graduation did not remember the name: %+v", first.Item.Entry.Experiment)
	}
	demote, err := f.service.PlanDemote(t.Context(), experiment.DemoteRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	returned, err := f.service.ApplyDemote(t.Context(), demote)
	if err != nil {
		t.Fatal(err)
	}
	if returned.Item.Entry.Name != "prototype" || returned.Item.Entry.Experiment.GraduatedName != "chosen-project" {
		t.Fatalf("demotion conflated Try name and remembered project name: %+v", returned.Item.Entry)
	}
	if returned.Item.Entry.Experiment.GraduatedPath != first.Plan.Destination {
		t.Fatal("demotion replaced successful graduation history")
	}
	*f.now = f.now.Add(time.Hour)
	plan, err := f.service.PlanGraduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := filepath.EvalSymlinks(f.projects)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name != "chosen-project" || plan.Category != "" || plan.Destination != filepath.Join(projects, "chosen-project") {
		t.Fatalf("regraduation forgot the name or reused the category: %+v", plan)
	}
	second, err := f.service.ApplyGraduate(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	history := second.Item.Entry.Experiment
	if second.Item.ID != item.ID || history.GraduatedName != "chosen-project" || history.GraduatedPath != plan.Destination || !history.GraduatedAt.Equal(*f.now) {
		t.Fatalf("successful name/path/time were not recorded together: %+v", second.Item.Entry)
	}
}

func TestGraduateNameChangeInvalidatesExpectedCatalogEvenWithExplicitName(t *testing.T) {
	f := newFixture(t, experiment.Hooks{}, 1)
	source := f.mkdir("2026-09-20-prototype")
	items, _, err := f.service.List(t.Context(), experiment.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := itemByBase(t, items, filepath.Base(source))
	selected, err := f.registry.Update(item.ID, func(entry *catalog.Entry) error {
		entry.Experiment.GraduatedAt = f.now.Add(-time.Hour)
		entry.Experiment.GraduatedPath = filepath.Join(f.projects, "first-name")
		entry.Experiment.GraduatedName = "first-name"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := f.registry.Update(item.ID, func(entry *catalog.Entry) error {
		entry.Experiment.GraduatedName = "second-name"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PlanGraduate(t.Context(), experiment.GraduateRequest{
		Ref: item.ID, Name: "explicit-name", Expected: selected,
	}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed remembered name was omitted from catalog equality: %v", err)
	}
	if _, err := f.service.PlanGraduate(t.Context(), experiment.GraduateRequest{
		Ref: item.ID, Name: "explicit-name", Expected: fresh,
	}); err != nil {
		t.Fatalf("fresh selected record was rejected: %v", err)
	}
}
