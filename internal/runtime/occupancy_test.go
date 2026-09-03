package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

type occupancySessionRuntime struct {
	sessions []runtime.Session
	listErr  error
}

func (*occupancySessionRuntime) Name() string    { return "test" }
func (*occupancySessionRuntime) Available() bool { return true }
func (*occupancySessionRuntime) Open(context.Context, string, string) (runtime.OpenResult, error) {
	return runtime.OpenResult{}, nil
}
func (*occupancySessionRuntime) Close(context.Context, string) error { return nil }
func (r *occupancySessionRuntime) List(context.Context) ([]runtime.Session, error) {
	return r.sessions, r.listErr
}
func (*occupancySessionRuntime) Annotate(context.Context, string, map[string]string) error {
	return nil
}

type occupancyActivityRuntime struct {
	*occupancySessionRuntime
	activities  []runtime.AgentActivity
	activityErr error
}

func (r *occupancyActivityRuntime) AgentActivities(context.Context) ([]runtime.AgentActivity, error) {
	return r.activities, r.activityErr
}

type occupancyResolvingRuntime struct {
	*occupancyActivityRuntime
	currentPane  string
	currentErr   error
	currentCalls int
}

func (r *occupancyResolvingRuntime) CurrentPaneID(context.Context) (string, error) {
	r.currentCalls++
	return r.currentPane, r.currentErr
}

func TestInspectOccupancyCanonicalizesExactGitWorktreeCoverage(t *testing.T) {
	repo := gittest.New(t)
	nested := filepath.Join(repo.Root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	repo.Git("branch", "feat/linked")
	repo.Git("worktree", "add", linked, "feat/linked")
	other := t.TempDir()
	alias := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(repo.Root, alias); err != nil {
		t.Fatal(err)
	}

	rt := &occupancyActivityRuntime{
		occupancySessionRuntime: &occupancySessionRuntime{sessions: []runtime.Session{
			{Handle: "live", Panes: []runtime.Pane{{ID: "live:p1", CWD: nested}}},
			{Handle: "remembered", Panes: []runtime.Pane{{ID: "remembered:p1", CWD: other}}},
		}},
		activities: []runtime.AgentActivity{
			{PaneID: "live:p1", WorkspaceID: "live", Agent: "claude", Status: "working", CWD: nested},
			{PaneID: "linked:p1", WorkspaceID: "linked", Agent: "claude", Status: "working", CWD: linked},
			{PaneID: "other:p1", WorkspaceID: "other", Agent: "claude", Status: "working", CWD: other},
		},
	}
	got, err := runtime.InspectOccupancy(context.Background(), rt, alias, runtime.OccupancyOptions{
		Profile: runtime.OccupancyStrict,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTarget, err := filepath.EvalSymlinks(repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != filepath.Clean(wantTarget) {
		t.Fatalf("target = %q, want %q", got.Target, filepath.Clean(wantTarget))
	}
	if !got.SessionList.Observed() || !got.AgentActivityList.Observed() {
		t.Fatalf("observations = sessions %+v, activities %+v", got.SessionList, got.AgentActivityList)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Runtime.Handle != "live" {
		t.Fatalf("covering sessions = %+v; a non-covering remembered handle must not count", got.Sessions)
	}
	if len(got.Agents) != 1 || got.Agents[0].Activity.PaneID != "live:p1" {
		t.Fatalf("covering agents = %+v; linked and unrelated worktrees must not count", got.Agents)
	}
	if got.Agents[0].SessionHandle != "live" || !got.Agents[0].Blocking {
		t.Fatalf("agent correlation = %+v", got.Agents[0])
	}
}

func TestInspectOccupancyDoesNotTreatNestedWorktreeSessionAsMainCoverage(t *testing.T) {
	repo := gittest.New(t)
	linked := filepath.Join(repo.Root, ".claude", "worktrees", "nested")
	if err := os.MkdirAll(filepath.Dir(linked), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.Git("branch", "feat/nested")
	repo.Git("worktree", "add", linked, "feat/nested")
	rt := &occupancySessionRuntime{sessions: []runtime.Session{{
		Handle: "nested", Panes: []runtime.Pane{{ID: "nested:p1", CWD: linked}},
	}}}

	main, err := runtime.InspectOccupancy(context.Background(), rt, repo.Root, runtime.OccupancyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(main.Sessions) != 0 {
		t.Fatalf("nested Git worktree session covered canonical checkout: %+v", main.Sessions)
	}
	child, err := runtime.InspectOccupancy(context.Background(), rt, linked, runtime.OccupancyOptions{})
	if err != nil || len(child.Sessions) != 1 || child.Sessions[0].Runtime.Handle != "nested" {
		t.Fatalf("nested worktree exact coverage = %+v, %v", child.Sessions, err)
	}
}

func TestInspectOccupancyCleanupBlocksNestedRepositoryInsideLinkedTarget(t *testing.T) {
	repo := gittest.New(t)
	linked := filepath.Join(t.TempDir(), "linked")
	repo.Git("branch", "feat/linked-nested")
	repo.Git("worktree", "add", linked, "feat/linked-nested")
	nested := filepath.Join(linked, "ignored-nested-repo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	repo.GitIn(nested, "init", "--initial-branch=main")

	rt := &occupancyActivityRuntime{
		occupancySessionRuntime: &occupancySessionRuntime{sessions: []runtime.Session{{
			Handle: "nested", Panes: []runtime.Pane{{ID: "nested:p1", CWD: nested}},
		}}},
		activities: []runtime.AgentActivity{{
			PaneID: "nested:p1", WorkspaceID: "nested", Agent: "claude", Status: "working", CWD: nested,
		}},
	}
	cleanup, err := runtime.InspectOccupancy(context.Background(), rt, linked, runtime.OccupancyOptions{
		Profile: runtime.OccupancyCleanup,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup.Sessions) != 1 || len(cleanup.Agents) != 1 || !cleanup.Agents[0].Blocking {
		t.Fatalf("nested repository cleanup occupancy=%+v", cleanup)
	}
	strict, err := runtime.InspectOccupancy(context.Background(), rt, linked, runtime.OccupancyOptions{
		Profile: runtime.OccupancyStrict,
	})
	if err != nil || len(strict.Sessions) != 0 || len(strict.Agents) != 0 {
		t.Fatalf("nested repository strict occupancy=%+v err=%v", strict, err)
	}
}

func TestInspectOccupancyUsesRecognizedPaneFallbackWhenActivityListerIsUnsupported(t *testing.T) {
	target := t.TempDir()
	newRuntime := func() *occupancySessionRuntime {
		return &occupancySessionRuntime{sessions: []runtime.Session{{
			Handle: "w1",
			Panes: []runtime.Pane{{
				ID: "w1:p1", CWD: target, Agent: "claude", AgentStatus: "idle",
			}},
		}}}
	}
	strict, err := runtime.InspectOccupancy(context.Background(), newRuntime(), target, runtime.OccupancyOptions{
		Profile: runtime.OccupancyStrict,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strict.AgentActivityList.Supported || !strict.AgentActivityList.Unknown() {
		t.Fatalf("unsupported activity capability was reported as observed: %+v", strict.AgentActivityList)
	}
	if len(strict.Agents) != 1 || !strict.Agents[0].Blocking {
		t.Fatalf("strict pane fallback = %+v", strict.Agents)
	}

	cleanup, err := runtime.InspectOccupancy(context.Background(), newRuntime(), target, runtime.OccupancyOptions{
		Profile: runtime.OccupancyCleanup,
	})
	if err != nil || len(cleanup.Agents) != 1 || cleanup.Agents[0].Blocking {
		t.Fatalf("cleanup pane fallback = %+v, %v", cleanup.Agents, err)
	}
}

func TestInspectOccupancyStrictAndCleanupProfilesForEveryAgentStatus(t *testing.T) {
	target := t.TempDir()
	cases := []struct {
		status          string
		cleanupBlocking bool
	}{
		{status: "working", cleanupBlocking: true},
		{status: "running", cleanupBlocking: true},
		{status: "busy", cleanupBlocking: true},
		{status: "blocked", cleanupBlocking: true},
		{status: "waiting", cleanupBlocking: true},
		{status: "idle", cleanupBlocking: false},
		{status: "done", cleanupBlocking: false},
		{status: "unknown", cleanupBlocking: true},
		{status: "", cleanupBlocking: true},
		{status: "paused", cleanupBlocking: true},
	}
	for _, tc := range cases {
		name := tc.status
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			newRuntime := func() *occupancyActivityRuntime {
				return &occupancyActivityRuntime{
					occupancySessionRuntime: &occupancySessionRuntime{sessions: []runtime.Session{{
						Handle: "w1", Panes: []runtime.Pane{{ID: "w1:p1", CWD: target}},
					}}},
					activities: []runtime.AgentActivity{{
						PaneID: "w1:p1", WorkspaceID: "w1", Agent: "claude", Status: tc.status, CWD: target,
					}},
				}
			}
			cleanup, err := runtime.InspectOccupancy(context.Background(), newRuntime(), target, runtime.OccupancyOptions{
				Profile: runtime.OccupancyCleanup,
			})
			if err != nil || len(cleanup.Agents) != 1 {
				t.Fatalf("cleanup inspection = %+v, %v", cleanup, err)
			}
			if cleanup.Agents[0].Blocking != tc.cleanupBlocking {
				t.Fatalf("cleanup blocking for %q = %v, want %v", tc.status, cleanup.Agents[0].Blocking, tc.cleanupBlocking)
			}
			strict, err := runtime.InspectOccupancy(context.Background(), newRuntime(), target, runtime.OccupancyOptions{
				Profile: runtime.OccupancyStrict, CloseUnknown: true,
			})
			if err != nil || len(strict.Agents) != 1 || !strict.Agents[0].Blocking {
				t.Fatalf("strict inspection for %q = %+v, %v", tc.status, strict, err)
			}
		})
	}

	for _, status := range []string{"", "unknown"} {
		rt := &occupancyActivityRuntime{
			occupancySessionRuntime: &occupancySessionRuntime{sessions: []runtime.Session{{
				Handle: "w1", Panes: []runtime.Pane{{ID: "w1:p1", CWD: target}},
			}}},
			activities: []runtime.AgentActivity{{
				PaneID: "w1:p1", WorkspaceID: "w1", Agent: "claude", Status: status, CWD: target,
			}},
		}
		got, err := runtime.InspectOccupancy(context.Background(), rt, target, runtime.OccupancyOptions{
			Profile: runtime.OccupancyCleanup, CloseUnknown: true,
		})
		if err != nil || len(got.Agents) != 1 || got.Agents[0].Blocking {
			t.Fatalf("acknowledged cleanup unknown %q = %+v, %v", status, got, err)
		}
	}
}

func TestInspectOccupancyResolvesMovedCallerForStrictExclusionAndCleanupContainment(t *testing.T) {
	target := t.TempDir()
	newRuntime := func() *occupancyResolvingRuntime {
		return &occupancyResolvingRuntime{
			occupancyActivityRuntime: &occupancyActivityRuntime{
				occupancySessionRuntime: &occupancySessionRuntime{sessions: []runtime.Session{{
					Handle: "w2", Panes: []runtime.Pane{{ID: "w2:p-new", CWD: target}},
				}}},
				activities: []runtime.AgentActivity{{
					PaneID: "w2:p-new", WorkspaceID: "w2", Agent: "claude", Status: "working", CWD: target,
				}},
			},
			currentPane: "w2:p-new",
		}
	}

	strictRT := newRuntime()
	strict, err := runtime.InspectOccupancy(context.Background(), strictRT, target, runtime.OccupancyOptions{
		Profile: runtime.OccupancyStrict, CallerWorkspaceID: "w1", CallerPaneID: "w1:p-old",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strictRT.currentCalls != 1 || !strict.CurrentPane.Observed() || strict.CallerPaneID != "w2:p-new" {
		t.Fatalf("strict caller resolution = %+v, calls=%d", strict, strictRT.currentCalls)
	}
	if len(strict.Sessions) != 1 || !strict.Sessions[0].IsCaller {
		t.Fatalf("moved caller session was not identified: %+v", strict.Sessions)
	}
	if len(strict.Agents) != 1 || !strict.Agents[0].IsCaller || strict.Agents[0].Blocking {
		t.Fatalf("strict current caller must be excluded: %+v", strict.Agents)
	}

	cleanupRT := newRuntime()
	cleanup, err := runtime.InspectOccupancy(context.Background(), cleanupRT, target, runtime.OccupancyOptions{
		Profile: runtime.OccupancyCleanup, CallerWorkspaceID: "w1", CallerPaneID: "w1:p-old",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup.Sessions) != 1 || !cleanup.Sessions[0].IsCaller || len(cleanup.Agents) != 1 || !cleanup.Agents[0].Blocking {
		t.Fatalf("cleanup must retain caller containment: sessions=%+v agents=%+v", cleanup.Sessions, cleanup.Agents)
	}
}

func TestInspectOccupancyRetainsIndependentObservationErrors(t *testing.T) {
	target := t.TempDir()
	listErr := errors.New("session inventory unavailable")
	activityErr := errors.New("agent inventory unavailable")
	paneErr := errors.New("current pane unavailable")
	rt := &occupancyResolvingRuntime{
		occupancyActivityRuntime: &occupancyActivityRuntime{
			occupancySessionRuntime: &occupancySessionRuntime{listErr: listErr},
			activityErr:             activityErr,
		},
		currentErr: paneErr,
	}
	got, err := runtime.InspectOccupancy(context.Background(), rt, target, runtime.OccupancyOptions{
		Profile: runtime.OccupancyStrict, CallerPaneID: "w1:p-old",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(got.SessionList.Err, listErr) || !got.SessionList.Unknown() || !got.SessionList.Attempted {
		t.Fatalf("session observation = %+v", got.SessionList)
	}
	if !errors.Is(got.AgentActivityList.Err, activityErr) || !got.AgentActivityList.Unknown() || !got.AgentActivityList.Attempted {
		t.Fatalf("activity observation = %+v", got.AgentActivityList)
	}
	if !errors.Is(got.CurrentPane.Err, paneErr) || !got.CurrentPane.Unknown() || !got.CurrentPane.Attempted || got.CallerPaneID != "" {
		t.Fatalf("current-pane observation = %+v, effective pane %q", got.CurrentPane, got.CallerPaneID)
	}
}

func TestInspectOccupancySeparatesSessionCoverageErrorsFromListErrors(t *testing.T) {
	rt := &occupancySessionRuntime{sessions: []runtime.Session{{
		Handle: "w1", Panes: []runtime.Pane{{ID: "w1:p1", CWD: "\x00"}},
	}}}
	got, err := runtime.InspectOccupancy(context.Background(), rt, t.TempDir(), runtime.OccupancyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.SessionList.Observed() || got.SessionList.Err != nil || got.SessionCoverageErr == nil {
		t.Fatalf("session list = %+v, coverage error = %v", got.SessionList, got.SessionCoverageErr)
	}
}

func TestInspectOccupancyNoneIsExplicitlyUnobserved(t *testing.T) {
	got, err := runtime.InspectOccupancy(context.Background(), runtime.None{}, t.TempDir(), runtime.OccupancyOptions{
		Profile: runtime.OccupancyStrict, CallerPaneID: "reported:p1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != "none" || len(got.Sessions) != 0 || len(got.Agents) != 0 {
		t.Fatalf("none occupancy = %+v", got)
	}
	for name, observation := range map[string]runtime.OccupancyObservation{
		"sessions":     got.SessionList,
		"agents":       got.AgentActivityList,
		"current pane": got.CurrentPane,
	} {
		if observation.Supported || observation.Attempted || observation.Observed() || !observation.Unknown() || observation.Err != nil {
			t.Errorf("none %s observation = %+v; want unsupported and unknown", name, observation)
		}
	}
}
