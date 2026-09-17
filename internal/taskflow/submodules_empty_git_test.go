package taskflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/submodule"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func emptySubmoduleLifecycleFixture(t *testing.T) *lifecycleGitFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newLifecycleGitFixture(t, task.ModeWorktree, task.Done)
	child := gittest.New(t)
	mustGitCommand(t, f.repo, "submodule", "add", child.Root, "child")
	mustGitCommand(t, f.repo, "commit", "-am", "test: empty submodule")
	mustGitCommand(t, f.worktree, "merge", "--ff-only", "main")
	return f
}

func submoduleTestImplementation(t *testing.T, f *lifecycleGitFixture, hooks LifecycleHooks) *lifecycleService {
	t.Helper()
	rt := lifecycleObservedEmptyRuntime{}
	impl, err := newLifecycleImplementation(LifecycleConfig{
		Config: f.cfg, Tasks: f.tasks, Artifacts: f.artifacts,
		DefaultRuntime: func() runtime.Runtime { return rt }, NamedRuntime: func(string) runtime.Runtime { return rt },
		Host: "test-host", CWD: f.root, CallerWorkspaceID: "test-caller-workspace", CallerPaneID: "test-caller-pane",
		Clock: func() time.Time { return time.Unix(1_700_000_000, 0) }, Hooks: hooks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return impl
}

func emptySubmoduleIntent(t *testing.T, f *lifecycleGitFixture, checkout, source string, status artifact.Status) artifact.Intent {
	t.Helper()
	repo, err := gitx.Discover(t.Context(), f.repo)
	if err != nil {
		t.Fatal(err)
	}
	intent := artifact.Intent{
		RunID: "empty-submodule-test", Provider: "claude", SessionID: "00000000-0000-0000-0000-000000000001",
		RepoPath: source, WorktreePath: checkout, GitCommonDir: repo.GitCommonDir,
		Branch: "feature", Head: mustGitCommand(t, f.repo, "rev-parse", "HEAD"),
	}
	if err := f.artifacts.Create(t.Context(), &intent); err != nil {
		t.Fatal(err)
	}
	if status != artifact.Armed {
		if err := f.artifacts.Update(t.Context(), intent.ID, func(i *artifact.Intent) error { i.Status = status; i.ArtifactCommit = i.Head; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	return intent
}

func TestCleanupRegressionSubmoduleEmptyRetirementAndConsentPreview(t *testing.T) {
	for _, state := range []string{"empty", "absent", "scaffolding"} {
		t.Run(state, func(t *testing.T) {
			f := emptySubmoduleLifecycleFixture(t)
			if state == "absent" {
				if err := os.Remove(filepath.Join(f.worktree, "child")); err != nil {
					t.Fatal(err)
				}
			}
			if state == "scaffolding" {
				repo, err := gitx.Discover(t.Context(), f.worktree)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(repo.GitDir, "modules", "old", "empty"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			preview, err := f.service.Plan(t.Context(), f.request(t, RetireOptions{}))
			if err != nil || preview.Availability == AvailabilityReady {
				t.Fatalf("recursive consent missing: %v %+v", err, preview.Conditions())
			}
			authority := RetirementPreviewAuthority(preview)
			if authority.Map()["submodule-removal"] == "" {
				t.Fatal("preview omitted empty layout authority")
			}
			p, err := f.service.Plan(t.Context(), f.request(t, RetireOptions{Recursive: true, PreviewAuthority: authority}))
			status, statusErr := gitx.StatusOf(t.Context(), f.worktree)
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			if status.Dirty() {
				if state != "absent" || err != nil || p.Availability == AvailabilityReady {
					t.Fatalf("tracked missing gitlink must block: %v %+v", err, p.Conditions())
				}
				result, applyErr := f.service.Apply(t.Context(), p, Approve(p.PlanID))
				if applyErr == nil || len(result.AttemptedSteps()) != 0 {
					t.Fatalf("dirty outer caused cleanup effects: %v %+v", applyErr, result)
				}
				return
			}
			if err != nil || p.Availability != AvailabilityReady {
				t.Fatalf("unchanged consent preview stale: %v %+v", err, p.Conditions())
			}
			for _, effect := range p.Effects() {
				if effect.Code == effectVerifySubmodules && effect.Network {
					t.Fatal("empty local observation mislabeled as network proof")
				}
			}
			t.Setenv("GIT_ALLOW_PROTOCOL", "none")
			result, err := f.service.Apply(t.Context(), p, Approve(p.PlanID))
			if err != nil || result.Milestone != MilestoneRetired || result.PartialSuccess {
				t.Fatalf("retire empty: %v %+v", err, result)
			}
			if _, err := f.tasks.Get(f.record.Task.ID); !errors.Is(err, task.ErrNotFound) {
				t.Fatal("task not retired", err)
			}
			if _, err := os.Stat(f.worktree); !os.IsNotExist(err) {
				t.Fatal("outer remains", err)
			}
		})
	}
}

func TestCleanupRegressionSubmodulePreviewBindsAbsentAndEmptyIdentity(t *testing.T) {
	for _, mutation := range []string{"absence-presence", "replace-empty", "new-admin"} {
		t.Run(mutation, func(t *testing.T) {
			f := emptySubmoduleLifecycleFixture(t)
			child := filepath.Join(f.worktree, "child")
			if mutation == "absence-presence" {
				if err := os.Remove(child); err != nil {
					t.Fatal(err)
				}
			}
			preview, err := f.service.Plan(t.Context(), f.request(t, RetireOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			before := mustGitCommand(t, f.worktree, "status", "--porcelain")
			switch mutation {
			case "absence-presence":
				if err := os.Mkdir(child, 0700); err != nil {
					t.Fatal(err)
				}
			case "replace-empty":
				if err := os.Rename(child, filepath.Join(f.root, "old-empty")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(child, 0700); err != nil {
					t.Fatal(err)
				}
			case "new-admin":
				repo, err := gitx.Discover(t.Context(), f.worktree)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(repo.GitDir, "modules"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if after := mustGitCommand(t, f.worktree, "status", "--porcelain"); mutation != "absence-presence" && before != after {
				t.Fatal("fixture changed parent status")
			}
			p, err := f.service.Plan(t.Context(), f.request(t, RetireOptions{Recursive: true, PreviewAuthority: RetirementPreviewAuthority(preview)}))
			if err != nil {
				t.Fatal(err)
			}
			c, exists := conditionByCode(p, "retirement-preview-current")
			if !exists || c.Verdict != VerdictBlocked || (mutation != "absence-presence" && !strings.Contains(c.Evidence, "submodule-removal")) {
				t.Fatalf("changed layout consent accepted: %+v", p.Conditions())
			}
			if p.AuthorityFields()["submodule-removal"] == preview.AuthorityFields()["submodule-removal"] {
				t.Fatal("absence/directory identity change omitted from layout authority")
			}
		})
	}
}

func TestCleanupRegressionSubmoduleEmptyArtifactClaims(t *testing.T) {
	for _, scope := range []string{"exact", "subtree", "source", "unrelated"} {
		for _, status := range []artifact.Status{artifact.Armed, artifact.Finalized, artifact.Discarded} {
			t.Run(scope+"-"+string(status), func(t *testing.T) {
				f := emptySubmoduleLifecycleFixture(t)
				child := filepath.Join(f.worktree, "child")
				checkout, source := child, f.repo
				switch scope {
				case "subtree":
					checkout = filepath.Join(child, "nested", "missing")
				case "source":
					checkout, source = filepath.Join(f.root, "other"), child
				case "unrelated":
					checkout = filepath.Join(f.root, "unrelated")
				}
				impl := submoduleTestImplementation(t, f, LifecycleHooks{InspectArtifacts: func(context.Context, *artifact.Store, string) (artifact.ReadinessInspection, error) {
					t.Fatal("Git artifact readiness called for empty child")
					return artifact.ReadinessInspection{}, nil
				}})
				g, err := gitx.SubmodulesOf(t.Context(), f.worktree)
				if err != nil {
					t.Fatal(err)
				}
				before, err := impl.submoduleClaims(t.Context(), g, f.record.Task.ID)
				if err != nil {
					t.Fatal(err)
				}
				emptySubmoduleIntent(t, f, checkout, source, status)
				after, err := impl.submoduleClaims(t.Context(), g, f.record.Task.ID)
				blocked := scope != "unrelated" && status != artifact.Discarded
				if (err != nil) != blocked {
					t.Fatalf("claims blocked=%t error=%v", blocked, err)
				}
				if scope == "unrelated" && before != after {
					t.Fatal("unrelated intent changed child authority")
				}
				if scope != "unrelated" && before == after {
					t.Fatal("matching intent (including discarded) omitted from authority")
				}
			})
		}
	}
}

func TestCleanupRegressionSubmoduleEmptyClaimsStrictErrors(t *testing.T) {
	for _, kind := range []string{"malformed", "unreadable", "identity"} {
		t.Run(kind, func(t *testing.T) {
			f := emptySubmoduleLifecycleFixture(t)
			switch kind {
			case "malformed":
				if err := os.MkdirAll(f.artifacts.Dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.artifacts.Dir, "broken.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.MkdirAll(filepath.Dir(f.artifacts.Dir), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.artifacts.Dir, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			case "identity":
				emptySubmoduleIntent(t, f, "relative/ownership", f.repo, artifact.Discarded)
			}
			g, err := gitx.SubmodulesOf(t.Context(), f.worktree)
			if err != nil {
				t.Fatal(err)
			}
			impl := submoduleTestImplementation(t, f, LifecycleHooks{})
			if _, err := impl.submoduleClaims(t.Context(), g, f.record.Task.ID); err == nil || !strings.Contains(err.Error(), "child") {
				t.Fatalf("unknown %s artifact authority accepted: %v", kind, err)
			}
		})
	}
}

func TestCleanupRegressionSubmoduleLateTaskAndArtifactClaims(t *testing.T) {
	for _, kind := range []string{"task", "artifact"} {
		t.Run(kind, func(t *testing.T) {
			f := emptySubmoduleLifecycleFixture(t)
			p, err := f.service.Plan(t.Context(), f.request(t, RetireOptions{Recursive: true}))
			if err != nil || p.Availability != AvailabilityReady {
				t.Fatalf("plan: %v %+v", err, p.Conditions())
			}
			child := filepath.Join(f.worktree, "child")
			if kind == "artifact" {
				emptySubmoduleIntent(t, f, child, f.repo, artifact.Finalized)
			} else {
				candidate := task.Task{Name: "child work", Repo: "child", RepoPath: filepath.Join(child, "nested"), Branch: "main", Base: "main", Mode: task.ModeDirect, State: task.Warm, Owner: "test-host"}
				if _, err := f.tasks.Create(t.Context(), &candidate); err != nil {
					t.Fatal(err)
				}
			}
			result, err := f.service.Apply(t.Context(), p, Approve(p.PlanID))
			if err == nil || len(result.CompletedSteps()) != 0 {
				t.Fatalf("late claim applied: %v %+v", err, result)
			}
			if _, err := os.Stat(child); err != nil {
				t.Fatal("claimed empty checkout removed", err)
			}
			if _, err := f.tasks.Get(f.record.Task.ID); err != nil {
				t.Fatal("parent task removed", err)
			}
		})
	}
}

func TestCleanupRegressionSubmodulePartialPruneLedger(t *testing.T) {
	f := emptySubmoduleLifecycleFixture(t)
	repo, err := gitx.Discover(t.Context(), f.worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo.GitDir, "modules", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("outer cleanup fixture failure")
	impl := submoduleTestImplementation(t, f, LifecycleHooks{RemoveWorktree: func(context.Context, string, string, bool) error { return cause }})
	service := NewService(Handlers{Retire: impl.retireHandler()})
	p, err := service.Plan(t.Context(), f.request(t, RetireOptions{Recursive: true}))
	if err != nil || p.Availability != AvailabilityReady {
		t.Fatalf("plan: %v %+v", err, p.Conditions())
	}
	result, err := service.Apply(t.Context(), p, Approve(p.PlanID))
	var pruned *submodule.PrunedDirectoriesError
	if !errors.As(err, &pruned) || !errors.Is(err, cause) || !result.PartialSuccess || result.Milestone == MilestoneRetired {
		t.Fatalf("partial effect lost: %v %+v", err, result)
	}
	found := false
	for _, step := range result.AttemptedSteps() {
		if step.Effect.Code == EffectRemoveWorktree && step.Status == StepFailed && strings.Contains(step.Failure, "modules/empty") {
			found = true
		}
		if step.Effect.Code == EffectDeleteTask {
			t.Fatal("task deletion attempted after partial cleanup")
		}
	}
	if !found || len(result.Recovery()) == 0 {
		t.Fatal("pruned paths missing from ledger/recovery")
	}
	if _, err := os.Stat(f.worktree); err != nil {
		t.Fatal("outer unexpectedly removed", err)
	}
	if _, err := f.tasks.Get(f.record.Task.ID); err != nil {
		t.Fatal("task unexpectedly removed", err)
	}
}

func TestCleanupRegressionSubmoduleInitializedArtifactObservationCause(t *testing.T) {
	f := submoduleLifecycleFixture(t)
	cause := errors.New("fixture artifact receipt observation failed")
	impl := submoduleTestImplementation(t, f, LifecycleHooks{InspectArtifacts: func(context.Context, *artifact.Store, string) (artifact.ReadinessInspection, error) {
		return artifact.ReadinessInspection{}, cause
	}})
	g, err := gitx.SubmodulesOf(t.Context(), f.worktree)
	if err != nil {
		t.Fatal(err)
	}
	_, err = impl.submoduleClaims(t.Context(), g, f.record.Task.ID)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "child") || strings.Contains(err.Error(), "unfinished") {
		t.Fatalf("original artifact cause lost: %v", err)
	}
}

func TestCleanupRegressionSubmoduleDetachedUnrelatedFinalizedArtifact(t *testing.T) {
	f := submoduleLifecycleFixture(t)
	other := gittest.New(t)
	intent := emptySubmoduleIntent(t, f, other.Root, other.Root, artifact.Finalized)
	otherRepo, err := gitx.Discover(t.Context(), other.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.artifacts.Update(t.Context(), intent.ID, func(i *artifact.Intent) error {
		i.GitCommonDir = otherRepo.GitCommonDir
		i.Branch = "main"
		i.Head = mustGitCommand(t, other.Root, "rev-parse", "HEAD")
		i.ArtifactCommit = i.Head
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := f.service.Plan(t.Context(), f.request(t, RetireOptions{Recursive: true}))
	if err != nil || p.Availability != AvailabilityReady {
		t.Fatalf("unrelated finalized artifact blocked detached child: %v %+v", err, p.Conditions())
	}
	if _, err := f.service.Apply(t.Context(), p, Approve(p.PlanID)); err != nil {
		t.Fatal(err)
	}
}
