package taskflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func submoduleLifecycleFixture(t *testing.T) *lifecycleGitFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	child := gittest.New(t)
	mustGitCommand(t, f.repo, "submodule", "add", child.Root, "child")
	mustGitCommand(t, f.repo, "commit", "-am", "test: submodule")
	mustGitCommand(t, f.worktree, "merge", "--ff-only", "main")
	if _, err := gitx.InitSubmodules(t.Context(), f.worktree); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSubmoduleRetirementRequiresRecursiveAndRejectsStaleChild(t *testing.T) {
	f := submoduleLifecycleFixture(t)
	p, err := f.service.Plan(t.Context(), f.request(t, RetireOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Availability == AvailabilityReady {
		t.Fatal("recursive disposal implicitly approved")
	}
	p, err = f.service.Plan(t.Context(), f.request(t, RetireOptions{Recursive: true}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Availability != AvailabilityReady {
		t.Fatalf("not ready: %+v", p.Conditions())
	}
	mustGitCommand(t, filepath.Join(f.worktree, "child"), "branch", "new-local-ref", "HEAD")
	result, err := f.service.Apply(t.Context(), p, Approve(p.PlanID))
	if err == nil || len(result.CompletedSteps()) != 0 {
		t.Fatalf("stale child applied: %+v %v", result, err)
	}
}

func TestSubmoduleRetirementCompletesInsideOut(t *testing.T) {
	f := submoduleLifecycleFixture(t)
	p, err := f.service.Plan(t.Context(), f.request(t, RetireOptions{Recursive: true}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Availability != AvailabilityReady {
		t.Fatalf("not ready: %+v", p.Conditions())
	}
	result, err := f.service.Apply(t.Context(), p, Approve(p.PlanID))
	if err != nil {
		t.Fatalf("Apply: %v %+v", err, result)
	}
	if result.Milestone != MilestoneRetired {
		t.Fatalf("result: %+v", result)
	}
	if _, err = os.Stat(f.worktree); !os.IsNotExist(err) {
		t.Fatal("checkout remains")
	}
	if _, err = gitx.Discover(t.Context(), filepath.Join(f.repo, "child")); err != nil {
		t.Fatal("canonical child damaged", err)
	}
}

func TestSubmoduleRetirementPreviewRejectsChangedChildRefs(t *testing.T) {
	for _, unmanaged := range []bool{false, true} {
		name := "managed"
		if unmanaged {
			name = "unmanaged"
		}
		t.Run(name, func(t *testing.T) {
			f := submoduleLifecycleFixture(t)
			checkout := f.worktree
			request := func(preview Fields) Request {
				return f.request(t, RetireOptions{Recursive: true, PreviewAuthority: preview})
			}
			if unmanaged {
				checkout = filepath.Join(f.root, "unmanaged-submodules")
				locator := addUnmanagedCheckout(t, f, "unmanaged-submodules", checkout, false)
				if _, err := gitx.InitSubmodules(t.Context(), checkout); err != nil {
					t.Fatal(err)
				}
				request = func(preview Fields) Request {
					r, err := NewRequest(locator, RemoveCheckoutOptions{Recursive: true, PreviewAuthority: preview})
					if err != nil {
						t.Fatal(err)
					}
					return r
				}
			}
			plan, err := f.service.Plan(t.Context(), request(Fields{}))
			if err != nil || plan.Availability != AvailabilityReady {
				t.Fatalf("initial plan: %v %+v", err, plan.Conditions())
			}
			preview := RetirementPreviewAuthority(plan)
			for _, key := range []string{"submodules", "submodule-intent", "submodule-claims"} {
				if _, exists := preview.Map()[key]; !exists {
					t.Fatalf("preview omitted %s", key)
				}
			}
			before := mustGitCommand(t, checkout, "status", "--porcelain")
			mustGitCommand(t, filepath.Join(checkout, "child"), "branch", "after-preview", "HEAD")
			if after := mustGitCommand(t, checkout, "status", "--porcelain"); before != after {
				t.Fatal("fixture changed parent Git status instead of only child refs")
			}
			fresh, err := f.service.Plan(t.Context(), request(preview))
			if err != nil {
				t.Fatal(err)
			}
			condition, exists := conditionByCode(fresh, "retirement-preview-current")
			if !exists || condition.Verdict != VerdictBlocked || !strings.Contains(condition.Evidence, "submodules changed") {
				t.Fatalf("stale child preview was not blocked: %+v", fresh.Conditions())
			}
			result, err := f.service.Apply(t.Context(), fresh, Approve(fresh.PlanID))
			if err == nil || len(result.CompletedSteps()) != 0 {
				t.Fatalf("stale preview produced effects: %+v %v", result, err)
			}
			if _, err := os.Stat(filepath.Join(checkout, "child")); err != nil {
				t.Fatalf("stale preview changed checkout: %v", err)
			}
		})
	}
}
