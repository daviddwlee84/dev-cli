package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/testutil"
)

// Retain the activityRuntime dispatch/annotation spies while recording the exact
// checkout and distinguishing worktree opening from generic runtime opening.
type wtOpenRuntime struct {
	*activityRuntime
	openPaths     []string
	worktreePaths []string
	beforeActive  func()
}

func (r *wtOpenRuntime) Open(ctx context.Context, dir, label string) (runtime.OpenResult, error) {
	r.openPaths = append(r.openPaths, dir)
	return r.activityRuntime.Open(ctx, dir, label)
}

func (r *wtOpenRuntime) OpenWorktree(ctx context.Context, dir, label string) (runtime.OpenResult, error) {
	r.worktreePaths = append(r.worktreePaths, dir)
	return r.activityRuntime.OpenWorktree(ctx, dir, label)
}

func (r *wtOpenRuntime) Activate(ctx context.Context, handle string) error {
	if r.beforeActive != nil {
		r.beforeActive()
	}
	return r.activityRuntime.Activate(ctx, handle)
}

type wtOpenFixture struct {
	t      *testing.T
	repo   *gittest.Repo
	path   string
	app    *App
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newWtOpenFixture(t *testing.T, rt runtime.Runtime) *wtOpenFixture {
	t.Helper()
	t.Setenv("DEV_SHELL_CD_FILE", "")
	t.Setenv("DEV_SHELL_CD_FD", "")
	r := gittest.New(t)
	testutil.SetHome(t, filepath.Dir(r.Root))
	// This checkout was created externally, not by dev's worktree manager.
	path := filepath.Join(filepath.Dir(r.Root), "external checkout")
	r.Git("worktree", "add", "-b", "feat/visible", path, "main")
	linked := &gittest.Repo{T: t, Root: path}
	// Controller-owned checkout paths are canonical native paths, even when
	// Git spells the same Windows directory with forward slashes.
	var err error
	path, err = pathx.Canonical(linked.Git("rev-parse", "--show-toplevel"))
	if err != nil {
		t.Fatal(err)
	}
	linked.Root = path
	linked.Write("README.md", "staged change\n")
	linked.Git("add", "README.md")
	linked.Write("README.md", "staged plus unstaged change\n")
	linked.Write("untracked.txt", "untracked bytes\n")
	r.Write("README.md", "canonical dirty bytes\n")

	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	store := task.NewStore(cfg.TasksDir())
	if err := store.Save(&task.Task{
		Name: "existing task", Repo: "repo", RepoPath: r.Root, Branch: "main", Base: "main",
		Mode: task.ModeDirect, State: task.Warm, Next: "keep existing intent",
		RuntimeName: "herdr", RuntimeHandle: "w-existing",
	}); err != nil {
		t.Fatal(err)
	}
	f := &wtOpenFixture{t: t, repo: r, path: path}
	f.app = &App{Cfg: cfg, Tasks: store, Out: &f.stdout, Err: &f.stderr, runtimeInstance: rt, colorMode: "never"}
	return f
}

func (f *wtOpenFixture) run(args ...string) error {
	f.t.Helper()
	cmd := newWtOpenCmd(f.app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(append([]string{"feat/visible", "--repo", f.repo.Root}, args...))
	return cmd.Execute()
}

// Compare bytes as well as Git observations: status alone would miss an index
// rewrite or a task metadata update that happened to preserve its meaning.
func (f *wtOpenFixture) snapshot() map[string]string {
	f.t.Helper()
	got := map[string]string{
		"refs":      f.repo.Git("show-ref"),
		"worktrees": f.repo.Git("worktree", "list", "--porcelain"),
	}
	for i, dir := range []string{f.repo.Root, f.path} {
		prefix := fmt.Sprintf("checkout-%d/", i)
		got[prefix+"HEAD"] = f.repo.GitIn(dir, "rev-parse", "HEAD")
		got[prefix+"branch"] = f.repo.GitIn(dir, "symbolic-ref", "HEAD")
		got[prefix+"status"] = f.repo.GitIn(dir, "--no-optional-locks", "status", "--porcelain=v1", "-z")
		index := f.repo.GitIn(dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
		data, err := os.ReadFile(index)
		if err != nil {
			f.t.Fatal(err)
		}
		got[prefix+"index"] = string(data)
	}
	for i, root := range []string{f.repo.Root, f.path, f.app.Cfg.Paths.StateDir} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			got[fmt.Sprintf("files-%d/%s", i, rel)] = string(data)
			return nil
		})
		if err != nil {
			f.t.Fatal(err)
		}
	}
	return got
}

func (f *wtOpenFixture) assertUnchanged(before map[string]string) {
	f.t.Helper()
	if after := f.snapshot(); !reflect.DeepEqual(after, before) {
		f.t.Fatalf("opening changed checkout, refs, index, worktree registration or private task state\nbefore: %v\nafter: %v", before, after)
	}
}

func TestWtOpenDefaultActivates(t *testing.T) {
	for _, args := range [][]string{nil, {"--no-focus=false"}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			rt := &wtOpenRuntime{activityRuntime: &activityRuntime{openResult: runtime.OpenResult{
				Handle: "w-visible", Surface: "worktree", Opened: true, Created: true,
			}}}
			f := newWtOpenFixture(t, rt)
			wantOutput := fmt.Sprintf("feat/visible  %s  (herdr w-visible)\n", config.Contract(f.path))
			rt.beforeActive = func() {
				if got := f.stdout.String(); got != wantOutput {
					t.Fatalf("output before activation = %q, want %q", got, wantOutput)
				}
			}
			before := f.snapshot()
			if err := f.run(args...); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rt.activateCalls, []string{"w-visible"}) {
				t.Fatalf("activation calls = %v", rt.activateCalls)
			}
			f.assertUnchanged(before)
		})
	}
}

func TestWtOpenNoFocus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  runtime.OpenResult
		generic bool
		want    string
	}{
		{"created", runtime.OpenResult{Handle: "w-new", Surface: "worktree", Opened: true, Created: true, RootPaneID: "w-new:p1"}, false, "runtime opened (surface=\"worktree\")"},
		{"reused", runtime.OpenResult{Handle: "w-existing", Surface: "worktree", Opened: true}, false, "runtime reused (surface=\"worktree\")"},
		{"fallback", runtime.OpenResult{Handle: "w-fallback", Surface: "workspace", Opened: true, Created: true}, false, "runtime opened (surface=\"workspace\")"},
		{"generic-session", runtime.OpenResult{Handle: "session-visible", Surface: "session", Opened: true}, true, "runtime reused (surface=\"session\")"},
		{"not-opened", runtime.OpenResult{Handle: "hint-only"}, false, "runtime not opened (surface=\"\")"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &wtOpenRuntime{activityRuntime: &activityRuntime{openResult: tc.result}}
			var backend runtime.Runtime = rt
			if tc.generic {
				rt.name = "tmux"
				// Hide WorktreeOpener to exercise openCheckout's generic path.
				backend = struct{ runtime.Runtime }{rt}
			}
			f := newWtOpenFixture(t, backend)
			before := f.snapshot()
			if err := f.run("--no-focus"); err != nil {
				t.Fatal(err)
			}
			paths, otherPaths := rt.worktreePaths, rt.openPaths
			if tc.generic {
				paths, otherPaths = rt.openPaths, rt.worktreePaths
			}
			if !reflect.DeepEqual(paths, []string{f.path}) || len(otherPaths) != 0 || rt.openCalls != 1 ||
				!reflect.DeepEqual(rt.openLabels, []string{"repo/feat/visible"}) {
				t.Fatalf("open target: paths=%v other=%v calls=%d labels=%v", paths, otherPaths, rt.openCalls, rt.openLabels)
			}
			if len(rt.activateCalls) != 0 || len(rt.runCalls) != 0 || len(rt.annotations) != 0 ||
				len(rt.closeCalls) != 0 || len(rt.closePaneCalls) != 0 || rt.activityCalls != 0 {
				t.Fatalf("visibility-only open requested extra runtime effects: %+v", rt.activityRuntime)
			}
			want := fmt.Sprintf("feat/visible  %s  (%s %s)\n   %s; focus not requested\n",
				config.Contract(f.path), rt.Name(), tc.result.Handle, tc.want)
			if got := f.stdout.String(); got != want || f.stderr.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q, want %q", got, f.stderr.String(), want)
			}
			f.assertUnchanged(before)
		})
	}
}

func TestWtOpenNoFocusNone(t *testing.T) {
	for _, channel := range []string{"printable", "file", "descriptor", "dashboard"} {
		t.Run(channel, func(t *testing.T) {
			f := newWtOpenFixture(t, runtime.None{})
			file := filepath.Join(t.TempDir(), "shell-cd")
			const sentinel = "leave shell handoff untouched"
			if err := os.WriteFile(file, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			switch channel {
			case "file":
				t.Setenv("DEV_SHELL_CD_FILE", file)
			case "descriptor":
				// Fails on every OS if cdDirective is reached; no native FD needed.
				t.Setenv("DEV_SHELL_CD_FD", "invalid")
			case "dashboard":
				f.app.workflowHandoff = func(func() error) error {
					t.Fatal("no-focus must not queue a shell handoff")
					return nil
				}
			}
			before := f.snapshot()
			if err := f.run("--no-focus"); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("feat/visible  %s\n   no runtime opened; no shell directory change requested\n", config.Contract(f.path))
			if got := f.stdout.String(); got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != sentinel {
				t.Fatalf("shell handoff modified: %q, %v", data, err)
			}
			f.assertUnchanged(before)
		})
	}
}

func TestWtOpenDefaultNoneChangesShellDirectory(t *testing.T) {
	f := newWtOpenFixture(t, runtime.None{})
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("feat/visible  %s\ncd %s\n", config.Contract(f.path), shellQuote(f.path))
	if got := f.stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestWtOpenNoFocusRuntimeError(t *testing.T) {
	wantErr := errors.New("runtime open failed; partial surface may remain")
	rt := &wtOpenRuntime{activityRuntime: &activityRuntime{
		openErr: wantErr, openResult: runtime.OpenResult{Handle: "partial", Surface: "workspace"},
	}}
	f := newWtOpenFixture(t, rt)
	before := f.snapshot()
	if err := f.run("--no-focus"); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want original runtime failure", err)
	}
	if f.stdout.Len() != 0 || len(rt.activateCalls) != 0 || len(rt.runCalls) != 0 || len(rt.annotations) != 0 {
		t.Fatalf("failed open reported success or requested more effects: stdout=%q runtime=%+v", f.stdout.String(), rt.activityRuntime)
	}
	f.assertUnchanged(before)
}

func TestWtOpenNoFocusMissingWorktree(t *testing.T) {
	rt := &wtOpenRuntime{activityRuntime: &activityRuntime{}}
	f := newWtOpenFixture(t, rt)
	before := f.snapshot()
	cmd := newWtOpenCmd(f.app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"missing", "--repo", f.repo.Root, "--no-focus"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `no worktree for branch "missing"`) {
		t.Fatalf("missing worktree error = %v", err)
	}
	if rt.openCalls != 0 || f.stdout.Len() != 0 {
		t.Fatal("missing worktree opened runtime or reported success")
	}
	f.assertUnchanged(before)
}
