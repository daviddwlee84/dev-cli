//go:build unix

package machineregistry

import (
	"context"
	"errors"
	"github.com/daviddwlee84/dev-cli/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestPermissionPlanExactRepairAndStaleUnion(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "repair", true: "stale"}[stale], func(t *testing.T) {
			store := testStore(t)
			applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
			dir := filepath.Dir(store.Path)
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(store.Path, 0o644); err != nil {
				t.Fatal(err)
			}
			plan, err := store.PlanPermissions(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Ready() || len(plan.Changes) != 2 {
				t.Fatalf("plan=%+v", plan)
			}
			info, _ := os.Stat(store.Path)
			if info.Mode().Perm() != 0o644 {
				t.Fatal("planning repaired permissions")
			}
			if stale {
				if err := os.Chmod(store.Path, 0o640); err != nil {
					t.Fatal(err)
				}
			}
			result, err := store.ApplyPermissions(t.Context(), plan)
			if stale {
				if !errors.Is(err, ErrStale) {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				info, _ := os.Stat(dir)
				if info.Mode().Perm() != 0o755 {
					t.Fatal("changed a path before rejecting stale union")
				}
				return
			}
			if err != nil || len(result.Outcomes) != 2 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if _, err := store.Read(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestPermissionPlanMissingDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "machines", "registry.db")
	store := NewStore(path)
	plan, err := store.PlanPermissions(t.Context())
	if err != nil || !plan.Ready() || plan.NeedsRepair() {
		t.Fatalf("%+v %v", plan, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plan created directory: %v", err)
	}
}

func TestPermissionPlanDirectoryActivityAndNewFileHardlink(t *testing.T) {
	for _, hardlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "sibling directory", true: "new file hardlink"}[hardlink], func(t *testing.T) {
			store := testStore(t)
			applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
			dir := filepath.Dir(store.Path)
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(store.Path, 0o644); err != nil {
				t.Fatal(err)
			}
			plan, err := store.PlanPermissions(t.Context())
			if err != nil || !plan.Ready() {
				t.Fatal(plan, err)
			}
			if hardlink {
				err = testutil.Link(t, store.Path, store.Path+".second")
			} else {
				// Other Go test packages also create sibling directories under
				// shared temporary ancestors while a permission plan is pending.
				err = os.Mkdir(filepath.Join(filepath.Dir(dir), "unrelated"), 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := store.ApplyPermissions(t.Context(), plan)
			if hardlink {
				if !errors.Is(err, ErrStale) || len(result.Outcomes) != 0 {
					t.Fatalf("hardlink must invalidate before effects: %+v, %v", result, err)
				}
				return
			}
			if err != nil || result.Status != "complete" || len(result.Outcomes) != 2 {
				t.Fatalf("unrelated directory activity invalidated exact metadata repair: %+v, %v", result, err)
			}
		})
	}
}

func TestPermissionPlanBlocksSymlinkAndHardlink(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			store := testStore(t)
			applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
			if kind == "symlink" {
				if err := os.Rename(store.Path, store.Path+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(store.Path+".real", store.Path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := testutil.Link(t, store.Path, store.Path+".second"); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := store.PlanPermissions(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if plan.Ready() || len(plan.Diagnostics) == 0 {
				t.Fatalf("unsafe plan: %+v", plan)
			}
			if _, err := store.ApplyPermissions(t.Context(), plan); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("apply: %v", err)
			}
		})
	}
}
func TestPathErrorPreservesPathAndSentinel(t *testing.T) {
	store := testStore(t)
	applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
	if err := os.Chmod(store.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := store.Read(t.Context())
	var path *PathError
	if !errors.Is(err, ErrUnsafePath) || !errors.As(err, &path) {
		t.Fatalf("%v", err)
	}
	if path.Path != store.Path || path.Mode.Perm() != 0o644 || path.ExpectedMode != 0o600 || !path.Repairable || path.Owner == "" {
		t.Fatalf("%+v", path)
	}
}

func TestPermissionPlanRejectsAlteredPreviewWithoutEffects(t *testing.T) {
	for _, change := range []string{"path", "before-mode", "after-mode", "remove-change", "add-diagnostic"} {
		t.Run(change, func(t *testing.T) {
			store := testStore(t)
			applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
			dir := filepath.Dir(store.Path)
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(store.Path, 0o644); err != nil {
				t.Fatal(err)
			}
			plan, err := store.PlanPermissions(t.Context())
			if err != nil || !plan.Ready() {
				t.Fatal(plan, err)
			}
			switch change {
			case "path":
				plan.Changes[0].Path = "/unreviewed"
			case "before-mode":
				plan.Changes[0].BeforeMode = 0o777
			case "after-mode":
				plan.Changes[0].AfterMode = 0o777
			case "remove-change":
				plan.Changes = plan.Changes[1:]
			case "add-diagnostic":
				plan.Diagnostics = []*PathError{{Path: store.Path, Reason: "altered"}}
			}
			if plan.Ready() || plan.NeedsRepair() {
				t.Fatal("altered preview advertised readiness")
			}
			result, err := store.ApplyPermissions(t.Context(), plan)
			if !errors.Is(err, ErrInvalidPlan) || len(result.Outcomes) != 0 {
				t.Fatalf("%+v %v", result, err)
			}
			for path, want := range map[string]os.FileMode{dir: 0o755, store.Path: 0o644} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != want {
					t.Fatalf("altered plan changed %s: %v", path, err)
				}
			}
		})
	}
}
func TestPermissionPlanCannotClearPrivateBlock(t *testing.T) {
	store := testStore(t)
	applyRequest(t, store, Request{Action: "adopt", MachineID: firstID, Label: "first"})
	if err := os.Chmod(filepath.Dir(store.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := testutil.Link(t, store.Path, store.Path+".second"); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanPermissions(t.Context())
	if err != nil || len(plan.Diagnostics) == 0 || len(plan.Changes) == 0 {
		t.Fatal(plan, err)
	}
	plan.Diagnostics = nil
	if plan.Ready() || plan.NeedsRepair() {
		t.Fatal("removed diagnostic cleared private block")
	}
	if _, err := store.ApplyPermissions(t.Context(), plan); !errors.Is(err, ErrInvalidPlan) {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Dir(store.Path))
	if info.Mode().Perm() != 0o755 {
		t.Fatal("blocked plan changed permissions")
	}
}
func TestPermissionPlanRejectsInvalidStoreAndContext(t *testing.T) {
	valid := NewStore(filepath.Join(t.TempDir(), "machines", "registry.db"))
	for _, store := range []*Store{nil, {}, {Path: "relative/registry.db"}, {Path: valid.Path + "/../registry.db"}, {Path: valid.Path + "\x00"}} {
		if _, err := store.PlanPermissions(t.Context()); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("invalid store plan: %+v %v", store, err)
		}
		if _, err := store.ApplyPermissions(t.Context(), PermissionPlan{}); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("invalid store apply: %+v %v", store, err)
		}
	}
	if _, err := valid.PlanPermissions(nil); !errors.Is(err, ErrInvalidPlan) {
		t.Fatal(err)
	}
	if _, err := valid.ApplyPermissions(nil, PermissionPlan{}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := valid.PlanPermissions(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	plan, err := valid.PlanPermissions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := valid.ApplyPermissions(canceled, plan); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
