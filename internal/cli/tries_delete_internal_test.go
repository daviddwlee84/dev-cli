package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestRemovalAssumptionNeverOverridesPartialKnownOccupation(t *testing.T) {
	path := t.TempDir()
	for _, assume := range []bool{false, true} {
		rt := &activityRuntime{sessions: []runtime.Session{{Handle: "occupied", Dirs: []string{path}}}, listErr: errors.New("partial enumeration")}
		a := &App{Tasks: task.NewStore(t.TempDir()), runtimeInstance: rt}
		if _, err := guardTryRemoval(t.Context(), a, path, assume); err == nil || !strings.Contains(err.Error(), "occupied") {
			t.Fatalf("assume=%v err=%v", assume, err)
		}
	}
}

func TestRemovalRuntimeWithoutPathsRemainsUnknown(t *testing.T) {
	a := &App{Tasks: task.NewStore(t.TempDir()), runtimeInstance: &activityRuntime{sessions: []runtime.Session{{Handle: "unknown"}}}}
	if _, err := guardTryRemoval(t.Context(), a, t.TempDir(), false); !errors.Is(err, errRemovalRuntimeUnknown) {
		t.Fatal(err)
	}
	if evidence, err := guardTryRemoval(t.Context(), a, t.TempDir(), true); err != nil || evidence != "unknown:explicit-assumption" {
		t.Fatalf("%q %v", evidence, err)
	}
}

func TestRemovalTaskClaimBlocksEvenWithAssumption(t *testing.T) {
	path := t.TempDir()
	store := task.NewStore(t.TempDir())
	tracked := &task.Task{Name: "claimed", Repo: "try", RepoPath: path, Branch: "main", Base: "main", Mode: task.ModeDirect, State: task.Warm}
	if err := store.Save(tracked); err != nil {
		t.Fatal(err)
	}
	a := &App{Tasks: store, runtimeInstance: runtime.None{}}
	if _, err := guardTryRemoval(t.Context(), a, path, true); err == nil || !strings.Contains(err.Error(), "claimed") {
		t.Fatal(err)
	}
	if _, err := guardTryRemoval(t.Context(), a, filepath.Dir(path), true); err == nil {
		t.Fatal("parent removal ignored contained task")
	}
}
