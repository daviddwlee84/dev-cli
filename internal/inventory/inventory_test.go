package inventory_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// fakeRuntime lets the join be tested without a multiplexer installed.
type fakeRuntime struct{ sessions []runtime.Session }

func (f *fakeRuntime) Name() string    { return "fake" }
func (f *fakeRuntime) Available() bool { return true }
func (f *fakeRuntime) Open(context.Context, string, string) (runtime.OpenResult, error) {
	return runtime.OpenResult{Handle: "h", Opened: true}, nil
}
func (f *fakeRuntime) Close(context.Context, string) error { return nil }
func (f *fakeRuntime) List(context.Context) ([]runtime.Session, error) {
	return f.sessions, nil
}
func (f *fakeRuntime) Annotate(context.Context, string, map[string]string) error { return nil }

func TestCollectEnrichesFromGit(t *testing.T) {
	r := gittest.New(t)
	tk := &task.Task{
		ID: "repo__main", Repo: "repo", RepoPath: r.Root,
		Branch: "main", State: task.Hot,
	}
	r.Write("dirty.txt", "x\n")

	rows := inventory.Collect(context.Background(), []*task.Task{tk}, runtime.None{}, inventory.Options{})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	got := rows[0]
	if !got.CheckoutExists {
		t.Error("CheckoutExists should be true")
	}
	if !got.Status.Dirty() {
		t.Error("the untracked file should make it dirty")
	}
	if got.LastCommit.IsZero() {
		t.Error("LastCommit should be read from HEAD")
	}
	if got.Checkout != r.Root {
		t.Errorf("Checkout = %q, want the repo path when there is no worktree", got.Checkout)
	}
}

func TestCollectUsesWorktreeAsCheckout(t *testing.T) {
	r := gittest.New(t)
	wtPath := filepath.Join(t.TempDir(), "feat-auth")
	if err := gitx.AddWorktree(context.Background(), r.Root, wtPath, "feat/auth", "main"); err != nil {
		t.Fatal(err)
	}
	tk := &task.Task{
		ID: "repo__feat-auth", Repo: "repo", RepoPath: r.Root,
		Branch: "feat/auth", WorktreePath: wtPath, State: task.Hot,
	}
	rows := inventory.Collect(context.Background(), []*task.Task{tk}, runtime.None{}, inventory.Options{})
	if rows[0].Checkout != wtPath {
		t.Errorf("Checkout = %q, want the worktree", rows[0].Checkout)
	}
	if rows[0].WorktreeMissing {
		t.Error("a registered worktree should not report as missing")
	}
}

func TestCollectDetectsMissingWorktree(t *testing.T) {
	r := gittest.New(t)
	tk := &task.Task{
		ID: "repo__gone", Repo: "repo", RepoPath: r.Root,
		Branch: "gone", WorktreePath: filepath.Join(t.TempDir(), "never-existed"), State: task.Warm,
	}
	rows := inventory.Collect(context.Background(), []*task.Task{tk}, runtime.None{}, inventory.Options{})
	if !rows[0].WorktreeMissing {
		t.Error("a worktree path that does not exist should be flagged")
	}
	if rows[0].StateDrift() == "" {
		t.Error("a missing worktree is drift worth reporting")
	}
}

func TestSessionMatchingPrefersExactDirectory(t *testing.T) {
	repoDir := t.TempDir()
	wtDir := filepath.Join(t.TempDir(), "wt")
	os.MkdirAll(wtDir, 0o755)

	rt := &fakeRuntime{sessions: []runtime.Session{
		{Handle: "repo-session", Dirs: []string{repoDir}},
		{Handle: "wt-session", Dirs: []string{wtDir}, AgentStatus: "working"},
	}}
	tasks := []*task.Task{
		{ID: "a", Repo: "r", RepoPath: repoDir, Branch: "main", State: task.Hot},
		{ID: "b", Repo: "r", RepoPath: repoDir, Branch: "feat/x", WorktreePath: wtDir, State: task.Hot},
	}

	rows := inventory.Collect(context.Background(), tasks, rt, inventory.Options{SkipGit: true})
	if rows[0].Session == nil || rows[0].Session.Handle != "repo-session" {
		t.Errorf("main checkout matched %+v", rows[0].Session)
	}
	if rows[1].Session == nil || rows[1].Session.Handle != "wt-session" {
		t.Errorf("worktree matched %+v", rows[1].Session)
	}
	if rows[1].Session.AgentStatus != "working" {
		t.Error("agent status should carry through")
	}
}

func TestStateDrift(t *testing.T) {
	dir := t.TempDir()
	live := &fakeRuntime{sessions: []runtime.Session{{Handle: "s", Dirs: []string{dir}}}}
	dead := &fakeRuntime{}

	hot := []*task.Task{{ID: "a", Repo: "r", RepoPath: dir, Branch: "main", State: task.Hot}}
	rows := inventory.Collect(context.Background(), hot, dead, inventory.Options{SkipGit: true})
	if rows[0].StateDrift() != "no live session" {
		t.Errorf("hot with no session should drift, got %q", rows[0].StateDrift())
	}
	rows = inventory.Collect(context.Background(), hot, live, inventory.Options{SkipGit: true})
	if rows[0].StateDrift() != "" {
		t.Errorf("hot with a session is consistent, got %q", rows[0].StateDrift())
	}

	warm := []*task.Task{{ID: "b", Repo: "r", RepoPath: dir, Branch: "main", State: task.Warm}}
	rows = inventory.Collect(context.Background(), warm, live, inventory.Options{SkipGit: true})
	if rows[0].StateDrift() != "session is live" {
		t.Errorf("warm with a session should drift, got %q", rows[0].StateDrift())
	}
}

func TestFilter(t *testing.T) {
	dir := t.TempDir()
	rt := &fakeRuntime{sessions: []runtime.Session{{Handle: "s", Dirs: []string{dir}}}}
	tasks := []*task.Task{
		{ID: "a", Repo: "api", RepoPath: dir, Branch: "main", State: task.Hot},
		{ID: "b", Repo: "web", RepoPath: t.TempDir(), Branch: "main", State: task.Warm},
		{ID: "c", Repo: "web", RepoPath: t.TempDir(), Branch: "main", State: task.Done},
	}
	rows := inventory.Collect(context.Background(), tasks, rt, inventory.Options{SkipGit: true})

	if got := (inventory.Filter{States: []task.State{task.Hot}}).Apply(rows); len(got) != 1 || got[0].Task.ID != "a" {
		t.Errorf("state filter: %+v", got)
	}
	if got := (inventory.Filter{Repo: "web"}).Apply(rows); len(got) != 2 {
		t.Errorf("repo filter: %d", len(got))
	}
	if got := (inventory.Filter{Repo: "WEB"}).Apply(rows); len(got) != 2 {
		t.Error("repo filter should be case-insensitive")
	}
	if got := (inventory.Filter{LiveOnly: true}).Apply(rows); len(got) != 1 || got[0].Task.ID != "a" {
		t.Errorf("live filter: %+v", got)
	}
	if got := (inventory.Filter{}).Apply(rows); len(got) != 3 {
		t.Error("an empty filter should match everything")
	}
}

func TestOrphans(t *testing.T) {
	claimed := t.TempDir()
	rt := &fakeRuntime{sessions: []runtime.Session{
		{Handle: "claimed", Label: "tracked", Dirs: []string{claimed}},
		{Handle: "loose", Label: "untracked", Dirs: []string{t.TempDir()}},
	}}
	tasks := []*task.Task{{ID: "a", Repo: "r", RepoPath: claimed, Branch: "main", State: task.Hot}}
	rows := inventory.Collect(context.Background(), tasks, rt, inventory.Options{SkipGit: true})

	sessions, _ := rt.List(context.Background())
	orphans := inventory.Orphans(sessions, rows)
	if len(orphans) != 1 || orphans[0].Handle != "loose" {
		t.Errorf("only the unclaimed session is an orphan, got %+v", orphans)
	}
}

func TestAgeFallsBackToUpdated(t *testing.T) {
	// A task with no checkout has no commits to age from, so the registry's
	// own timestamp has to stand in.
	tk := &task.Task{
		ID: "a", Repo: "r", RepoPath: filepath.Join(t.TempDir(), "gone"),
		Branch: "main", State: task.Cold,
		Updated: time.Now().Add(-48 * time.Hour),
	}
	rows := inventory.Collect(context.Background(), []*task.Task{tk}, runtime.None{}, inventory.Options{})
	if age := rows[0].Age(); age < 47*time.Hour || age > 49*time.Hour {
		t.Errorf("Age = %v, want about 48h", age)
	}
}

func TestCollectHandlesEmptyInput(t *testing.T) {
	rows := inventory.Collect(context.Background(), nil, runtime.None{}, inventory.Options{})
	if len(rows) != 0 {
		t.Errorf("want no rows, got %d", len(rows))
	}
}

// With a backend that cannot observe sessions, "hot but nothing live" is the
// only state it can produce — reporting it as drift would make `dev ls` cry
// wolf on every run.
func TestNoDriftWhenRuntimeCannotTrackSessions(t *testing.T) {
	dir := t.TempDir()
	hot := []*task.Task{{ID: "a", Repo: "r", RepoPath: dir, Branch: "main", State: task.Hot}}

	rows := inventory.Collect(context.Background(), hot, runtime.None{}, inventory.Options{SkipGit: true})
	if got := rows[0].StateDrift(); got != "" {
		t.Errorf("the none backend should produce no session drift, got %q", got)
	}

	rows = inventory.Collect(context.Background(), hot, &fakeRuntime{}, inventory.Options{
		SkipGit: true, SkipRuntime: true,
	})
	if got := rows[0].StateDrift(); got != "" {
		t.Errorf("skipping the runtime query should produce no session drift, got %q", got)
	}
}
