package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type startFixture struct {
	t      *testing.T
	repo   *gittest.Repo
	app    *App
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newStartFixture(t *testing.T, rt runtime.Runtime) *startFixture {
	t.Helper()
	r := gittest.New(t)
	cfg := config.Default()
	cfg.Paths.WorktreeRoot = filepath.Join(t.TempDir(), "Worktrees")
	cfg.Paths.WorktreePath = "{{worktree_root}}/{{repo}}/{{branch|slug}}"
	cfg.Worktree.Include = nil
	cfg.Worktree.PostCreate = config.PostCreate{}
	f := &startFixture{t: t, repo: r}
	f.app = &App{
		Cfg: cfg, Tasks: task.NewStore(filepath.Join(t.TempDir(), "tasks")),
		Out: &f.stdout, Err: &f.stderr, runtimeInstance: rt,
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(r.Root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	return f
}

func (f *startFixture) run(args ...string) error {
	f.t.Helper()
	cmd := newStartCmd(f.app)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	return cmd.Execute()
}

func decodeSingleStartJSON(t *testing.T, raw string) startJSON {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	var got startJSON
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("start output is not JSON: %v\n%s", err, raw)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("start output contains more than one JSON value: %v\n%s", err, raw)
	}
	return got
}

func TestStartJSONReturnsExactNestedHerdrWorktreeTarget(t *testing.T) {
	rt := &activityRuntime{openResult: runtime.OpenResult{
		Handle: "w-child", Surface: "worktree", Opened: true,
		Created: true, RootPaneID: "w-child:p9",
	}}
	f := newStartFixture(t, rt)

	if err := f.run("--task", "child task", "--branch", "feat/child", "--base", "main", "--json"); err != nil {
		t.Fatal(err)
	}
	got := decodeSingleStartJSON(t, f.stdout.String())
	if got.TaskID != "repo__feat-child" || got.Repo != "repo" || got.Branch != "feat/child" || got.Base != "main" || got.Mode != "worktree" {
		t.Fatalf("identity fields = %+v", got)
	}
	if !filepath.IsAbs(got.RepoPath) || !filepath.IsAbs(got.WorktreePath) || !filepath.IsAbs(got.Checkout) || got.Checkout != got.WorktreePath {
		t.Fatalf("paths must be absolute and checkout must be the worktree: %+v", got)
	}
	wantRuntime := startRuntimeJSON{
		Name: "herdr", Handle: "w-child", Surface: "worktree", Opened: true,
		Created: true, RootPaneID: "w-child:p9",
	}
	if got.Runtime != wantRuntime {
		t.Fatalf("runtime = %+v, want %+v", got.Runtime, wantRuntime)
	}
	if len(rt.openLabels) != 1 || rt.openLabels[0] != "repo/feat/child" {
		t.Fatalf("worktree label = %v, want native repo/branch grouping", rt.openLabels)
	}
	if len(rt.annotations) == 0 {
		t.Fatal("new child workspace should receive task metadata")
	}
	for i, annotation := range rt.annotations {
		if _, ok := annotation["origin_workspace"]; ok {
			t.Fatalf("special origin metadata must not be emitted: %v", annotation)
		}
		if rt.annotationHandles[i] != "w-child" {
			t.Fatalf("task annotation targeted %q instead of the child workspace", rt.annotationHandles[i])
		}
	}
	if !strings.Contains(f.stderr.String(), "worktree feat/child") {
		t.Fatalf("diagnostics should remain on stderr: %q", f.stderr.String())
	}
	saved, err := f.app.Tasks.Get(got.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.RuntimeHandle != "w-child" || saved.RuntimeName != "herdr" {
		t.Fatalf("saved runtime provenance = %+v", saved)
	}
}

func TestStartJSONNeverAdvertisesUnlaunchablePane(t *testing.T) {
	tests := []struct {
		name string
		rt   runtime.Runtime
	}{
		{name: "none", rt: runtime.None{}},
		{name: "tmux", rt: &activityRuntime{name: "tmux", openResult: runtime.OpenResult{
			Handle: "tmux-task", Surface: "session", Opened: true, Created: true, RootPaneID: "not-a-herdr-pane",
		}}},
		{name: "herdr reuse", rt: &activityRuntime{openResult: runtime.OpenResult{
			Handle: "w7", Surface: "worktree", Opened: true, RootPaneID: "stale-pane",
		}}},
		{name: "herdr fallback", rt: &activityRuntime{openResult: runtime.OpenResult{
			Handle: "w8", Surface: "workspace", Opened: true, Created: true, RootPaneID: "fallback-pane",
		}}},
		{name: "runtime failure", rt: &activityRuntime{openErr: errors.New("open failed")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newStartFixture(t, tc.rt)
			if err := f.run("--task", tc.name, "--base", "main", "--json"); err != nil {
				t.Fatal(err)
			}
			got := decodeSingleStartJSON(t, f.stdout.String())
			if got.Runtime.RootPaneID != "" {
				t.Fatalf("unlaunchable runtime advertised pane %q: %+v", got.Runtime.RootPaneID, got.Runtime)
			}
			if tc.name == "none" {
				if strings.Contains(f.stdout.String(), "\ncd ") {
					t.Fatalf("JSON stdout contains a shell directive: %q", f.stdout.String())
				}
				saved, err := f.app.Tasks.Get(got.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				if saved.RuntimeHandle != "" || saved.RuntimeName != "" {
					t.Fatalf("None pseudo-handle persisted: %+v", saved)
				}
			}
			if tc.name == "runtime failure" && (!strings.Contains(f.stderr.String(), "open failed") || got.Runtime.Opened) {
				t.Fatalf("runtime failure not represented honestly: stderr=%q runtime=%+v", f.stderr.String(), got.Runtime)
			}
		})
	}
}

func TestStartJSONSaveFailureEmitsNoSuccessObject(t *testing.T) {
	rt := &activityRuntime{openResult: runtime.OpenResult{Handle: "w7", Surface: "workspace", Opened: true, Created: true}}
	f := newStartFixture(t, rt)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.app.Tasks = task.NewStore(filepath.Join(blocked, "tasks"))

	if err := f.run("--task", "save failure", "--direct", "--json"); err == nil {
		t.Fatal("expected task save failure")
	}
	if f.stdout.Len() != 0 {
		t.Fatalf("failed start emitted success stdout: %q", f.stdout.String())
	}
	if rt.openCalls != 1 {
		t.Fatalf("runtime side effect should have happened before save failure, calls=%d", rt.openCalls)
	}
}

func TestStartHumanOutputAndDefaultLabelRemainCompatible(t *testing.T) {
	f := newStartFixture(t, runtime.None{})
	if err := f.run("--task", "quick fix", "--direct"); err != nil {
		t.Fatal(err)
	}
	out := f.stdout.String()
	if !strings.Contains(out, "quick fix  repo on main (direct)") || !strings.Contains(out, "\ncd ") {
		t.Fatalf("human output changed unexpectedly: %q", out)
	}
}

func TestStartDefaultWorktreeRemainsAllowedWhenSourceHasAgent(t *testing.T) {
	rt := &activityRuntime{openResult: runtime.OpenResult{
		Handle: "w-child", Surface: "worktree", Opened: true, Created: true, RootPaneID: "w-child:p1",
	}}
	f := newStartFixture(t, rt)
	rt.activities = []runtime.AgentActivity{{
		PaneID: "w-parent:p2", Agent: "claude", Status: "working", CWD: f.repo.Root,
	}}
	if err := f.run("--task", "independent", "--base", "main", "--json"); err != nil {
		t.Fatalf("new worktree creation should remain allowed: %v", err)
	}
	if rt.activityCalls != 0 {
		t.Fatalf("new worktree creation should not require sharing the source checkout, activity calls=%d", rt.activityCalls)
	}
}

func TestStartCollisionGuardsCanonicalCheckoutBeforeBranchSwitch(t *testing.T) {
	for _, args := range [][]string{
		{"--task", "direct", "--direct"},
		{"--task", "branch", "--branch-only", "--branch", "feat/branch", "--base", "main"},
	} {
		t.Run(args[1], func(t *testing.T) {
			rt := &activityRuntime{}
			f := newStartFixture(t, rt)
			rt.activities = []runtime.AgentActivity{{
				PaneID: "w1:p2", Agent: "claude", Status: "idle", CWD: f.repo.Root,
			}}
			if err := f.run(args...); err == nil || !strings.Contains(err.Error(), "already occupied") {
				t.Fatalf("collision should block start: %v", err)
			}
			if rt.openCalls != 0 {
				t.Fatal("runtime must not open after a collision")
			}
			if branch := f.repo.Git("branch", "--show-current"); branch != "main" {
				t.Fatalf("collision switched branch to %s", branch)
			}
		})
	}
}

func TestStartCollisionExcludesOnlyCallerPaneAndHonorsExplicitOverride(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	rt := &activityRuntime{activities: []runtime.AgentActivity{
		{PaneID: "w1:p1", Agent: "claude", Status: "working"},
	}}
	f := newStartFixture(t, rt)
	rt.activities[0].CWD = f.repo.Root
	if err := f.run("--task", "same pane", "--direct"); err != nil {
		t.Fatalf("caller pane should be excluded: %v", err)
	}

	rt2 := &activityRuntime{}
	f2 := newStartFixture(t, rt2)
	rt2.activities = []runtime.AgentActivity{{PaneID: "w1:p2", Agent: "claude", Status: "done", CWD: f2.repo.Root}}
	f2.app.allowSharedCheckout = true
	if err := f2.run("--task", "coordinated", "--direct"); err != nil {
		t.Fatalf("explicit override should proceed: %v", err)
	}
}

func TestAllowSharedCheckoutIsOnePersistentOverride(t *testing.T) {
	root := NewRootCommandWithIO(io.Discard, io.Discard)
	flag := root.PersistentFlags().Lookup("allow-shared-checkout")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("persistent override flag = %+v", flag)
	}
	start, _, err := root.Find([]string{"start"})
	if err != nil || start.InheritedFlags().Lookup("allow-shared-checkout") == nil {
		t.Fatalf("start must inherit the one root override: %v", err)
	}
	if start.LocalNonPersistentFlags().Lookup("allow-shared-checkout") != nil {
		t.Fatal("start must not define a duplicate local override")
	}
}
