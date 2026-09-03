package cli

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type sweepTestRuntime struct{ sessions []runtime.Session }

func (sweepTestRuntime) Name() string    { return "test" }
func (sweepTestRuntime) Available() bool { return true }
func (s sweepTestRuntime) List(context.Context) ([]runtime.Session, error) {
	return append([]runtime.Session(nil), s.sessions...), nil
}
func (sweepTestRuntime) Open(context.Context, string, string) (runtime.OpenResult, error) {
	return runtime.OpenResult{}, nil
}
func (sweepTestRuntime) Close(context.Context, string) error { return nil }
func (sweepTestRuntime) Annotate(context.Context, string, map[string]string) error {
	return nil
}

func newSweepTask(t *testing.T, mode task.CheckoutMode, state task.State) (*App, *task.Task) {
	t.Helper()
	store := task.NewStore(t.TempDir())
	repository := gittest.New(t)
	candidate := &task.Task{
		Repo: "repo", RepoPath: repository.Root, Branch: "main", Base: "main",
		Mode: mode, State: state,
	}
	if err := store.Save(candidate); err != nil {
		t.Fatal(err)
	}
	return &App{
		Cfg: config.Default(), Tasks: store, Out: io.Discard, Err: io.Discard,
		runtimeInstance: sweepTestRuntime{},
	}, candidate
}

func TestSweepDirectWarmNeverGoesCold(t *testing.T) {
	app, candidate := newSweepTask(t, task.ModeDirect, task.Warm)
	row := inventory.Row{Task: candidate, Checkout: candidate.RepoPath, CheckoutExists: true}

	suggestions := suggestFor(app, context.Background(), row, -time.Hour, sweepRetireOptions{})
	if len(suggestions) != 1 {
		t.Fatalf("suggestions = %+v", suggestions)
	}
	if suggestions[0].apply != nil || !strings.Contains(suggestions[0].reason, "direct tasks cannot go cold") {
		t.Fatalf("direct WARM suggestion = %+v", suggestions[0])
	}
}

func TestSweepRuntimeNoneDoesNotInferHotDrift(t *testing.T) {
	app, candidate := newSweepTask(t, task.ModeDirect, task.Hot)
	rows := inventory.Collect(context.Background(), []*task.Task{candidate}, runtime.None{}, inventory.Options{SkipGit: true})
	if drift := rows[0].StateDrift(); drift != "" {
		t.Fatalf("runtime none drift = %q", drift)
	}
	if suggestions := suggestFor(app, context.Background(), rows[0], 365*24*time.Hour, sweepRetireOptions{}); len(suggestions) != 0 {
		t.Fatalf("runtime none produced sweep suggestions: %+v", suggestions)
	}
}

func TestSweepHotWarmUsesRecordedRuntimeBackendAndClearsHints(t *testing.T) {
	app, candidate := newSweepTask(t, task.ModeDirect, task.Hot)
	currentRuntime := &activityRuntime{name: "tmux"}
	recordedRuntime := &stagedCloseRuntime{activityRuntime: &activityRuntime{
		name: "herdr",
		sessions: []runtime.Session{{
			Handle: "recorded-session", Dirs: []string{candidate.RepoPath}, AgentStatus: "done",
		}},
	}}
	candidate.RuntimeName = "herdr"
	candidate.RuntimeHandle = "recorded-session"
	if err := app.Tasks.Save(candidate); err != nil {
		t.Fatal(err)
	}
	app.runtimeInstance = currentRuntime
	app.runtimesByName = map[string]runtime.Runtime{"herdr": recordedRuntime}

	rows := inventory.Collect(context.Background(), []*task.Task{candidate}, currentRuntime, inventory.Options{SkipGit: true})
	if drift := rows[0].StateDrift(); drift != "no live session" {
		t.Fatalf("configured-backend drift=%q", drift)
	}
	suggestions := suggestFor(app, context.Background(), rows[0], 365*24*time.Hour, sweepRetireOptions{})
	if len(suggestions) != 1 || suggestions[0].apply == nil {
		t.Fatalf("HOT drift suggestions=%+v", suggestions)
	}
	if err := suggestions[0].apply(); err != nil {
		t.Fatalf("apply guarded warm parking: %v", err)
	}
	persisted, err := app.Tasks.Get(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != task.Warm || persisted.RuntimeName != "" || persisted.RuntimeHandle != "" {
		t.Fatalf("persisted task=%+v", persisted)
	}
	if !reflect.DeepEqual(recordedRuntime.closeCalls, []string{"recorded-session"}) {
		t.Fatalf("recorded runtime closes=%v", recordedRuntime.closeCalls)
	}
	if len(currentRuntime.closeCalls) != 0 {
		t.Fatalf("configured runtime was closed: %v", currentRuntime.closeCalls)
	}
}

func TestSweepDoesNotBlessTaskChangedAfterInventory(t *testing.T) {
	app, candidate := newSweepTask(t, task.ModeDirect, task.Hot)
	rows := inventory.Collect(context.Background(), []*task.Task{candidate}, sweepTestRuntime{}, inventory.Options{SkipGit: true})
	current, err := app.Tasks.GetRecord(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := current.Task
	changed.State = task.Done
	if _, err := app.Tasks.Update(context.Background(), &changed, current.Revision); err != nil {
		t.Fatal(err)
	}

	suggestions := suggestFor(app, context.Background(), rows[0], 365*24*time.Hour, sweepRetireOptions{})
	if len(suggestions) != 1 || suggestions[0].apply != nil ||
		!strings.Contains(suggestions[0].reason, "changed after the sweep inventory snapshot") {
		t.Fatalf("changed snapshot suggestion = %+v", suggestions)
	}
	persisted, err := app.Tasks.Get(candidate.ID)
	if err != nil || persisted.State != task.Done {
		t.Fatalf("current task was changed: %+v, %v", persisted, err)
	}
}

func TestSweepHotWarmApplyRejectsChangedTaskRevision(t *testing.T) {
	app, candidate := newSweepTask(t, task.ModeDirect, task.Hot)
	rows := inventory.Collect(context.Background(), []*task.Task{candidate}, sweepTestRuntime{}, inventory.Options{SkipGit: true})
	suggestions := suggestFor(app, context.Background(), rows[0], 365*24*time.Hour, sweepRetireOptions{})
	if len(suggestions) != 1 || suggestions[0].apply == nil {
		t.Fatalf("HOT drift suggestions = %+v", suggestions)
	}

	current, err := app.Tasks.GetRecord(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := current.Task
	changed.Next = "changed after report"
	if _, err := app.Tasks.Update(context.Background(), &changed, current.Revision); err != nil {
		t.Fatal(err)
	}
	if err := suggestions[0].apply(); !errors.Is(err, flow.ErrStalePlan) {
		t.Fatalf("stale sweep apply = %v, want ErrStalePlan", err)
	}
}

func TestSweepMissingRegistrationIsReportOnly(t *testing.T) {
	app, candidate := newSweepTask(t, task.ModeWorktree, task.Cold)
	repository := gittest.New(t)
	candidate.RepoPath = repository.Root
	candidate.WorktreePath = t.TempDir()
	if err := app.Tasks.Save(candidate); err != nil {
		t.Fatal(err)
	}
	row := inventory.Row{
		Task: candidate, Checkout: candidate.WorktreePath,
		CheckoutExists: false, WorktreeMissing: true,
	}

	suggestions := suggestFor(app, context.Background(), row, 365*24*time.Hour, sweepRetireOptions{})
	if len(suggestions) != 1 {
		t.Fatalf("suggestions = %+v", suggestions)
	}
	if suggestions[0].apply != nil || !strings.Contains(suggestions[0].action, "repository-wide prune scope") {
		t.Fatalf("missing registration suggestion = %+v", suggestions[0])
	}
}
