package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
)

func TestRetireCoordinatorPreservesRecursiveAndForegroundAuthority(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f, checkout := mergedTaskFixture(t, "recursive-handoff")
	child := gittest.New(t)
	f.repo.Git("submodule", "add", child.Root, "child")
	f.repo.Git("commit", "-am", "test: add child")
	f.repo.GitIn(checkout, "merge", "--ff-only", "main")
	if _, err := gitx.InitSubmodules(t.Context(), checkout); err != nil {
		t.Fatal(err)
	}
	selected, err := f.app.Tasks.Resolve("recursive-handoff")
	if err != nil {
		t.Fatal(err)
	}
	selected.State = task.Done
	if err := f.app.Tasks.Save(selected); err != nil {
		t.Fatal(err)
	}
	rt := f.app.runtimeInstance.(*activityRuntime)
	rt.name = "herdr"
	rt.openResult = runtime.OpenResult{Handle: "w9", Surface: "workspace", Opened: true, Created: true, RootPaneID: "w9:p1"}
	authority, err := captureRetirementAuthority(t.Context(), f.app, retireCommandTarget{Task: selected}, flow.RetireOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := retire.InspectForExternalCoordinator(t.Context(), rt, checkout, retire.Options{})
	if err != nil {
		t.Fatal(err)
	}
	closures := map[string]string{"w7": "approved-foreground-fingerprint"}
	handoffs := 0
	f.app.workflowHandoff = func(operation func() error) error {
		handoffs++
		return operation()
	}
	if err := launchExternalRetireCoordinator(t.Context(), f.app, rt, *selected, preview, false, false, closures, authority, true); err != nil {
		t.Fatal(err)
	}
	if handoffs != 1 || len(rt.runCalls) != 1 || rt.runCalls[0].PaneID != "w9:p1" {
		t.Fatalf("unexpected external handoff: %d %+v", handoffs, rt.runCalls)
	}
	directories, err := os.ReadDir(filepath.Join(f.app.Cfg.StateDir(), "retire-handoffs", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, entry := range directories {
		if entry.IsDir() && len(entry.Name()) == 32 {
			if id != "" {
				t.Fatal("multiple coordinator handoffs")
			}
			id = entry.Name()
		}
	}
	body, err := os.ReadFile(filepath.Join(retireHandoffDir(f.app, id), "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	var intent retireHandoffIntent
	if err := json.Unmarshal(body, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.Version != retireHandoffVersion || !intent.Recursive || intent.SubmoduleFingerprint == "" ||
		intent.SubmoduleFingerprint != authority.Map()["submodules"] ||
		!reflect.DeepEqual(intent.ProcessClosures, closures) || !reflect.DeepEqual(intent.PreviewAuthority, authority.Map()) {
		t.Fatalf("coordinator lost approved authority: %+v", intent)
	}
	f.repo.GitIn(filepath.Join(checkout, "child"), "branch", "after-handoff", "HEAD")
	err = runRetireCoordinator(t.Context(), f.app, id)
	if err == nil || !strings.Contains(err.Error(), "submodule graph changed") {
		t.Fatalf("changed child accepted by coordinator: %v", err)
	}
	if len(rt.closeCalls) != 0 {
		t.Fatalf("stale handoff closed runtime: %v", rt.closeCalls)
	}
	if _, err := os.Stat(filepath.Join(checkout, "child")); err != nil {
		t.Fatalf("stale handoff removed child: %v", err)
	}
	if _, err := f.app.Tasks.Get(selected.ID); err != nil {
		t.Fatalf("stale handoff removed task: %v", err)
	}
}

func TestRetireCoordinatorRecursiveWithoutChildren(t *testing.T) {
	f, checkout := mergedTaskFixture(t, "recursive-empty")
	selected, err := f.app.Tasks.Resolve("recursive-empty")
	if err != nil {
		t.Fatal(err)
	}
	selected.State = task.Done
	if err := f.app.Tasks.Save(selected); err != nil {
		t.Fatal(err)
	}
	rt := f.app.runtimeInstance.(*activityRuntime)
	rt.name = "herdr"
	rt.openResult = runtime.OpenResult{Handle: "w9", Surface: "workspace", Opened: true, Created: true, RootPaneID: "w9:p1"}
	authority, err := captureRetirementAuthority(t.Context(), f.app, retireCommandTarget{Task: selected}, flow.RetireOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := retire.InspectForExternalCoordinator(t.Context(), rt, checkout, retire.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := launchExternalRetireCoordinator(t.Context(), f.app, rt, *selected, preview, false, false, nil, authority, true); err != nil {
		t.Fatalf("recursive flag rejected a checkout with no children: %v", err)
	}
	directories, err := os.ReadDir(filepath.Join(f.app.Cfg.StateDir(), "retire-handoffs", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range directories {
		if entry.IsDir() && len(entry.Name()) == 32 {
			if err := runRetireCoordinator(t.Context(), f.app, entry.Name()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(checkout); !os.IsNotExist(err) {
				t.Fatalf("recursive coordinator did not retire checkout: %v", err)
			}
			return
		}
	}
	t.Fatal("no coordinator handoff created")
}
