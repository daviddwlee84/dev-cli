package task_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/task"
)

func newTask(repo, branch string, st task.State) *task.Task {
	return &task.Task{
		Repo:     repo,
		RepoPath: "/src/" + repo,
		Branch:   branch,
		Base:     "main",
		State:    st,
		Owner:    "test-host",
	}
}

func TestMakeID(t *testing.T) {
	for _, tc := range []struct{ repo, branch, want string }{
		{"atp-sipui", "fix/gx-security-recovery", "atp-sipui__fix-gx-security-recovery"},
		{"My Repo", "feat/a/b", "My-Repo__feat-a-b"},
		{"", "", "repo__branch"},
	} {
		if got := task.MakeID(tc.repo, tc.branch); got != tc.want {
			t.Errorf("MakeID(%q,%q) = %q, want %q", tc.repo, tc.branch, got, tc.want)
		}
	}
}

func TestSaveGetRoundTrip(t *testing.T) {
	s := task.NewStore(t.TempDir())
	in := newTask("atp-sipui", "fix/gx-security-recovery", task.Warm)
	in.Next = "finish refresh regression test"
	in.Tags = []string{"work", "urgent"}
	in.AgentSession = "claude:2136a917"
	in.Mode = task.ModeDirect

	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if in.ID != "atp-sipui__fix-gx-security-recovery" {
		t.Errorf("Save should derive the ID, got %q", in.ID)
	}
	if in.Created.IsZero() || in.Updated.IsZero() {
		t.Error("Save should stamp Created and Updated")
	}

	out, err := s.Get(in.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if out.Next != in.Next || out.Branch != in.Branch || out.State != task.Warm {
		t.Errorf("round trip mismatch: %+v", out)
	}
	if len(out.Tags) != 2 || out.AgentSession != "claude:2136a917" {
		t.Errorf("tags/session lost: %+v", out)
	}
	if out.Mode != task.ModeDirect {
		t.Errorf("mode lost: saved %q, loaded %q", in.Mode, out.Mode)
	}
}

func TestSaveIsAtomicAndPreservesCreated(t *testing.T) {
	dir := t.TempDir()
	s := task.NewStore(dir)
	in := newTask("r", "feat/x", task.Hot)
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	created := in.Created

	time.Sleep(1100 * time.Millisecond) // timestamps are second-truncated
	in.Next = "changed"
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	if !in.Created.Equal(created) {
		t.Error("Created must not move on re-save")
	}
	if !in.Updated.After(created) {
		t.Error("Updated should advance on re-save")
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") || strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	s := task.NewStore(t.TempDir())
	for name, tk := range map[string]*task.Task{
		"no branch": {Repo: "r", RepoPath: "/r", State: task.Hot},
		"no repo":   {Branch: "b", RepoPath: "/r", State: task.Hot},
		"no path":   {Repo: "r", Branch: "b", State: task.Hot},
		"bad state": {Repo: "r", Branch: "b", RepoPath: "/r", State: "lukewarm"},
	} {
		if err := s.Save(tk); err == nil {
			t.Errorf("%s: Save should have failed", name)
		}
	}
}

func TestListEmptyAndMissingDir(t *testing.T) {
	s := task.NewStore(filepath.Join(t.TempDir(), "never-created"))
	got, err := s.List()
	if err != nil {
		t.Fatalf("a missing state dir is an empty inventory, not an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %d", len(got))
	}
}

func TestListSortsHotFirst(t *testing.T) {
	s := task.NewStore(t.TempDir())
	for _, tk := range []*task.Task{
		newTask("c", "cold/one", task.Cold),
		newTask("a", "hot/one", task.Hot),
		newTask("d", "done/one", task.Done),
		newTask("b", "warm/one", task.Warm),
	} {
		if err := s.Save(tk); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []task.State{task.Hot, task.Warm, task.Cold, task.Done}
	if len(got) != 4 {
		t.Fatalf("want 4, got %d", len(got))
	}
	for i, w := range want {
		if got[i].State != w {
			t.Errorf("position %d = %s, want %s", i, got[i].State, w)
		}
	}
}

func TestListSkipsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	s := task.NewStore(dir)
	if err := s.Save(newTask("good", "feat/ok", task.Hot)); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("this is not = = toml"), 0o644)

	got, err := s.List()
	if err != nil {
		t.Fatalf("one corrupt file must not fail the whole listing: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("want the 1 good task, got %d", len(got))
	}
}

func TestResolve(t *testing.T) {
	s := task.NewStore(t.TempDir())
	a := newTask("atp-sipui", "fix/gx-security-recovery", task.Hot)
	a.Name = "atp security recovery"
	b := newTask("trading", "exp/orderbook-v2", task.Warm)
	for _, tk := range []*task.Task{a, b} {
		if err := s.Save(tk); err != nil {
			t.Fatal(err)
		}
	}

	for _, ref := range []string{
		a.ID,                       // exact id
		"fix/gx-security-recovery", // exact branch
		"security",                 // unique substring of the name
		"ATP",                      // case-insensitive
	} {
		got, err := s.Resolve(ref)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", ref, err)
		}
		if got.ID != a.ID {
			t.Errorf("Resolve(%q) = %q, want %q", ref, got.ID, a.ID)
		}
	}

	if _, err := s.Resolve("nothing-like-this"); !errors.Is(err, task.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestResolveAmbiguous(t *testing.T) {
	s := task.NewStore(t.TempDir())
	s.Save(newTask("repo", "feat/auth-ui", task.Hot))
	s.Save(newTask("repo", "feat/auth-api", task.Hot))

	_, err := s.Resolve("auth")
	if err == nil {
		t.Fatal("an ambiguous reference must be an error, not a silent pick")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should say ambiguous and list candidates, got %v", err)
	}
}

func TestFindByWorktreePrefersLongestMatch(t *testing.T) {
	s := task.NewStore(t.TempDir())
	repoTask := newTask("demo", "main", task.Hot)
	repoTask.RepoPath = "/src/demo"
	wtTask := newTask("demo", "feat/auth", task.Hot)
	wtTask.RepoPath = "/src/demo"
	wtTask.WorktreePath = "/wt/demo/feat-auth"
	s.Save(repoTask)
	s.Save(wtTask)

	got, err := s.FindByWorktree("/wt/demo/feat-auth/src/inner")
	if err != nil {
		t.Fatalf("FindByWorktree: %v", err)
	}
	if got.Branch != "feat/auth" {
		t.Errorf("inside a worktree the worktree task should win, got %q", got.Branch)
	}

	got, err = s.FindByWorktree("/src/demo/pkg")
	if err != nil || got.Branch != "main" {
		t.Errorf("inside the main checkout: got %+v err=%v", got, err)
	}

	if _, err := s.FindByWorktree("/somewhere/else"); !errors.Is(err, task.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	// A sibling directory sharing a name prefix must not match.
	if _, err := s.FindByWorktree("/src/demo-other"); !errors.Is(err, task.ErrNotFound) {
		t.Errorf("prefix match must respect path boundaries, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := task.NewStore(t.TempDir())
	tk := newTask("r", "feat/x", task.Done)
	s.Save(tk)
	if err := s.Delete(tk.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(tk.ID); !errors.Is(err, task.ErrNotFound) {
		t.Errorf("second delete should report ErrNotFound, got %v", err)
	}
}

func TestParseState(t *testing.T) {
	if got, err := task.ParseState(" HOT "); err != nil || got != task.Hot {
		t.Errorf("ParseState(HOT) = %q, %v", got, err)
	}
	if _, err := task.ParseState("lukewarm"); err == nil {
		t.Error("unknown state should error")
	}
}

func TestEffectiveModeInfersLegacyTasksConservatively(t *testing.T) {
	if got := (task.Task{WorktreePath: "/wt/x"}).EffectiveMode(); got != task.ModeWorktree {
		t.Errorf("legacy worktree mode = %q", got)
	}
	if got := (task.Task{}).EffectiveMode(); got != task.ModeBranch {
		t.Errorf("legacy no-path task should infer branch, never direct: %q", got)
	}
	if got := (task.Task{Mode: task.ModeDirect, WorktreePath: "/ignored"}).EffectiveMode(); got != task.ModeDirect {
		t.Errorf("explicit mode should win: %q", got)
	}
}

func TestValidateRejectsUnknownMode(t *testing.T) {
	tk := newTask("repo", "main", task.Hot)
	tk.Mode = "magic"
	if err := tk.Validate(); err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Errorf("unknown mode should fail, got %v", err)
	}
}
