package task_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
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

func TestRevisionCoversPersistedSemanticContent(t *testing.T) {
	base := exactImportTask()
	baseRevision := base.Revision()
	if len(baseRevision) != 64 {
		t.Fatalf("Revision length = %d, want 64 hex characters", len(baseRevision))
	}

	same := *base
	same.Tags = append([]string(nil), base.Tags...)
	same.Created = base.Created.In(time.FixedZone("created-offset", 9*60*60))
	same.Updated = base.Updated.In(time.FixedZone("updated-offset", -5*60*60))
	if got := same.Revision(); got != baseRevision {
		t.Fatalf("equal instants and semantic content produced %q, want %q", got, baseRevision)
	}

	mutations := []struct {
		name   string
		mutate func(*task.Task)
	}{
		{"ID", func(tk *task.Task) { tk.ID += "-other" }},
		{"Name", func(tk *task.Task) { tk.Name += " other" }},
		{"Repo", func(tk *task.Task) { tk.Repo += "-other" }},
		{"RepoPath", func(tk *task.Task) { tk.RepoPath += "-other" }},
		{"Branch", func(tk *task.Task) { tk.Branch += "-other" }},
		{"Base", func(tk *task.Task) { tk.Base += "-other" }},
		{"WorktreePath", func(tk *task.Task) { tk.WorktreePath += "-other" }},
		{"Mode", func(tk *task.Task) { tk.Mode = task.ModeDirect }},
		{"State", func(tk *task.Task) { tk.State = task.Cold }},
		{"Owner", func(tk *task.Task) { tk.Owner += "-other" }},
		{"Next", func(tk *task.Task) { tk.Next += " other" }},
		{"Note", func(tk *task.Task) { tk.Note += " other" }},
		{"Tags", func(tk *task.Task) { tk.Tags = []string{"different"} }},
		{"AgentSession", func(tk *task.Task) { tk.AgentSession += "-other" }},
		{"RuntimeHandle", func(tk *task.Task) { tk.RuntimeHandle += "-other" }},
		{"RuntimeName", func(tk *task.Task) { tk.RuntimeName += "-other" }},
		{"Created", func(tk *task.Task) { tk.Created = tk.Created.Add(time.Nanosecond) }},
		{"Updated", func(tk *task.Task) { tk.Updated = tk.Updated.Add(time.Nanosecond) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := *base
			changed.Tags = append([]string(nil), base.Tags...)
			mutation.mutate(&changed)
			if got := changed.Revision(); got == baseRevision {
				t.Errorf("Revision did not change after changing %s", mutation.name)
			}
		})
	}
}

func TestLegacyTOMLRemainsReadableWithoutPersistingRevision(t *testing.T) {
	dir := t.TempDir()
	const id = "legacy__feat-old"
	legacy := `name = "legacy"
repo = "legacy"
repo_path = "/src/legacy"
branch = "feat/old"
base = "main"
worktree_path = ""
state = "warm"
owner = "old-host"
next = "continue"
note = ""
tags = ["old"]
agent_session = ""
runtime_handle = ""
runtime_name = ""
created = 2025-01-02T03:04:05Z
updated = 2025-01-03T03:04:05Z
`
	path := filepath.Join(dir, id+".toml")
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	s := task.NewStore(dir)
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get legacy task: %v", err)
	}
	if got.ID != id || got.Mode != "" || got.EffectiveMode() != task.ModeBranch {
		t.Fatalf("legacy task decoded incorrectly: %+v", got)
	}
	if got.Revision() == "" {
		t.Fatal("legacy task should have a computed revision")
	}
	if err := s.Save(got); err != nil {
		t.Fatalf("Save legacy task: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "revision") {
		t.Fatalf("computed revision leaked into legacy TOML:\n%s", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("task mode = %04o, want 0644", got)
		}
	}
}

func TestSavePublishesPointerFieldsOnlyAfterDurableSuccess(t *testing.T) {
	dir := t.TempDir()
	s := task.NewStore(dir)
	in := newTask("repo", "feat/fail", task.Hot)
	in.Updated = time.Date(2020, 2, 3, 4, 5, 6, 7, time.UTC)
	before := *in

	id := task.MakeID(in.Repo, in.Branch)
	if err := os.Mkdir(filepath.Join(dir, id+".toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(in); err == nil {
		t.Fatal("Save should fail when the destination is a directory")
	}
	if !reflect.DeepEqual(*in, before) {
		t.Fatalf("failed Save mutated caller task:\n got %+v\nwant %+v", *in, before)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("failed Save left temporary file %q", entry.Name())
		}
	}
}

func TestTaskIDsCannotEscapeStore(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "tasks")
	s := task.NewStore(dir)
	invalid := []string{"", ".", "..", ".hidden", "../escape", "nested/name", `nested\name`, "/absolute", `C:\absolute`}
	for _, id := range invalid {
		t.Run(fmt.Sprintf("id_%q", id), func(t *testing.T) {
			if err := task.ValidateID(id); !errors.Is(err, task.ErrInvalidID) {
				t.Errorf("ValidateID(%q) = %v, want ErrInvalidID", id, err)
			}
			candidate := exactImportTask()
			candidate.ID = id
			if outcome, err := s.ImportExact(candidate); outcome != "" || !errors.Is(err, task.ErrInvalidID) {
				t.Errorf("ImportExact(%q) = %q, %v, want empty outcome and ErrInvalidID", id, outcome, err)
			}
			if id != "" {
				saved := newTask("repo", "feat/id", task.Hot)
				saved.ID = id
				if err := s.Save(saved); !errors.Is(err, task.ErrInvalidID) {
					t.Errorf("Save(%q) = %v, want ErrInvalidID", id, err)
				}
			}
			if _, err := s.Get(id); !errors.Is(err, task.ErrInvalidID) {
				t.Errorf("Get(%q) = %v, want ErrInvalidID", id, err)
			}
			if err := s.Delete(id); !errors.Is(err, task.ErrInvalidID) {
				t.Errorf("Delete(%q) = %v, want ErrInvalidID", id, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "escape.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path traversal created an outside task: %v", err)
	}
	if err := task.ValidateID("repo__feat-safe"); err != nil {
		t.Errorf("safe generated ID rejected: %v", err)
	}
}

func TestSaveRejectsStaleLoadedTask(t *testing.T) {
	s := task.NewStore(t.TempDir())
	original := newTask("repo", "feat/implicit-cas", task.Hot)
	if err := s.Save(original); err != nil {
		t.Fatal(err)
	}
	first, err := s.Get(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := s.Get(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	first.Next = "newer process"
	if err := s.Save(first); err != nil {
		t.Fatal(err)
	}
	stale.Next = "stale process"
	before := *stale
	if err := s.Save(stale); !errors.Is(err, task.ErrConflict) {
		t.Fatalf("stale Save = %v, want ErrConflict", err)
	}
	if !reflect.DeepEqual(*stale, before) {
		t.Fatal("failed stale Save mutated its caller")
	}
	stored, err := s.Get(original.ID)
	if err != nil || stored.Next != first.Next {
		t.Fatalf("stored task = %+v, %v", stored, err)
	}
}

func TestListWithDiagnosticsReportsIncompleteInventory(t *testing.T) {
	dir := t.TempDir()
	s := task.NewStore(dir)
	if err := s.Save(newTask("good", "feat/ok", task.Hot)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("not = valid = toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, diagnostics, err := s.ListWithDiagnostics()
	if err != nil || len(tasks) != 1 || len(diagnostics) != 1 {
		t.Fatalf("partial listing = tasks %d diagnostics %d error %v", len(tasks), len(diagnostics), err)
	}
	if filepath.Base(diagnostics[0].Path) != "broken.toml" || diagnostics[0].Err == nil {
		t.Fatalf("diagnostic = %+v", diagnostics[0])
	}
}

func TestReplaceDoneRequiresExactDoneRevisionAndStartsNewGeneration(t *testing.T) {
	s := task.NewStore(t.TempDir())
	completed := newTask("repo", "main", task.Done)
	completed.Name = "completed"
	if err := s.Save(completed); err != nil {
		t.Fatal(err)
	}
	oldCreated := completed.Created
	oldRevision := completed.Revision()

	time.Sleep(1100 * time.Millisecond)
	restarted := newTask("repo", "main", task.Hot)
	restarted.Name = "new direct task"
	if err := s.ReplaceDone(restarted, oldRevision); err != nil {
		t.Fatal(err)
	}
	if !restarted.Created.After(oldCreated) || restarted.Name != "new direct task" || restarted.State != task.Hot {
		t.Fatalf("restarted task = %+v", restarted)
	}
	stale := newTask("repo", "main", task.Hot)
	if err := s.ReplaceDone(stale, oldRevision); !errors.Is(err, task.ErrConflict) {
		t.Fatalf("stale ReplaceDone = %v, want ErrConflict", err)
	}
	loaded, err := s.Get(restarted.ID)
	if err != nil || loaded.Revision() != restarted.Revision() {
		t.Fatalf("stored restart = %+v, %v", loaded, err)
	}
}

func TestSaveIfRevisionRejectsStaleWriter(t *testing.T) {
	s := task.NewStore(t.TempDir())
	original := newTask("repo", "feat/cas", task.Hot)
	original.Next = "original"
	if err := s.Save(original); err != nil {
		t.Fatal(err)
	}
	staleRevision := original.Revision()

	newer := *original
	newer.Next = "newer process"
	if err := s.SaveIfRevision(&newer, staleRevision); err != nil {
		t.Fatalf("SaveIfRevision newer: %v", err)
	}

	stale := *original
	stale.Next = "stale process"
	before := stale
	if err := s.SaveIfRevision(&stale, staleRevision); !errors.Is(err, task.ErrConflict) {
		t.Fatalf("stale SaveIfRevision = %v, want ErrConflict", err)
	}
	if !reflect.DeepEqual(stale, before) {
		t.Fatalf("stale SaveIfRevision mutated caller: got %+v, want %+v", stale, before)
	}
	blankExpected := newer
	blankExpected.Next = "missing revision"
	if err := s.SaveIfRevision(&blankExpected, ""); !errors.Is(err, task.ErrConflict) {
		t.Fatalf("empty expected revision = %v, want ErrConflict", err)
	}

	got, err := s.Get(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Next != newer.Next || got.Revision() != newer.Revision() {
		t.Fatalf("stale writer replaced newer task: %+v", got)
	}
}

func TestSaveIfRevisionSerializesConcurrentWriters(t *testing.T) {
	s := task.NewStore(t.TempDir())
	original := newTask("repo", "feat/concurrent", task.Hot)
	if err := s.Save(original); err != nil {
		t.Fatal(err)
	}
	expected := original.Revision()

	const writers = 12
	type result struct {
		next string
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, writers)
	for i := range writers {
		go func(i int) {
			candidate := *original
			candidate.Next = fmt.Sprintf("writer-%02d", i)
			<-start
			err := s.SaveIfRevision(&candidate, expected)
			results <- result{next: candidate.Next, err: err}
		}(i)
	}
	close(start)

	successes := 0
	conflicts := 0
	winner := ""
	for range writers {
		result := <-results
		switch {
		case result.err == nil:
			successes++
			winner = result.next
		case errors.Is(result.err, task.ErrConflict):
			conflicts++
		default:
			t.Errorf("concurrent SaveIfRevision returned %v", result.err)
		}
	}
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("concurrent outcomes: successes=%d conflicts=%d, want 1 and %d", successes, conflicts, writers-1)
	}
	got, err := s.Get(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Next != winner {
		t.Fatalf("stored writer = %q, successful writer = %q", got.Next, winner)
	}
}

func TestImportExactCreateReplayAndConflict(t *testing.T) {
	dir := t.TempDir()
	s := task.NewStore(dir)
	source := exactImportTask()
	before := *source
	before.Tags = append([]string(nil), source.Tags...)

	outcome, err := s.ImportExact(source)
	if err != nil || outcome != task.ImportCreated {
		t.Fatalf("first ImportExact = %q, %v, want created", outcome, err)
	}
	if !reflect.DeepEqual(*source, before) {
		t.Fatalf("ImportExact changed source task: got %+v, want %+v", *source, before)
	}
	got, err := s.Get(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision() != source.Revision() || !got.Created.Equal(source.Created) || !got.Updated.Equal(source.Updated) {
		t.Fatalf("exact import changed semantic content: got %+v, want %+v", got, source)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, source.ID+".toml"))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("imported task mode = %04o, want 0644", got)
		}
	}

	replay := *source
	replay.Tags = append([]string(nil), source.Tags...)
	outcome, err = s.ImportExact(&replay)
	if err != nil || outcome != task.ImportIdentical {
		t.Fatalf("identical ImportExact = %q, %v, want identical", outcome, err)
	}

	conflicting := replay
	conflicting.Next = "different target content"
	outcome, err = s.ImportExact(&conflicting)
	if outcome != task.ImportConflict || !errors.Is(err, task.ErrConflict) {
		t.Fatalf("conflicting ImportExact = %q, %v, want conflict and ErrConflict", outcome, err)
	}
	got, err = s.Get(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision() != source.Revision() {
		t.Fatalf("conflicting import overwrote original: %+v", got)
	}
}

func TestDeleteWaitsForMutationLock(t *testing.T) {
	dir := t.TempDir()
	s := task.NewStore(dir)
	tk := newTask("repo", "feat/delete-lock", task.Done)
	if err := s.Save(tk); err != nil {
		t.Fatal(err)
	}

	held := make(chan struct{})
	release := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- lockx.WithDir(context.Background(), dir, "task test", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	started := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		close(started)
		deleteDone <- s.Delete(tk.ID)
	}()
	<-started
	select {
	case err := <-deleteDone:
		close(release)
		<-lockDone
		t.Fatalf("Delete returned before lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-lockDone; err != nil {
		t.Fatalf("release held lock: %v", err)
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("Delete after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Delete did not proceed after lock release")
	}
}

const (
	importHelperRootEnv  = "DEV_TASK_TEST_IMPORT_HELPER_ROOT"
	importHelperIndexEnv = "DEV_TASK_TEST_IMPORT_HELPER_INDEX"
)

func TestImportExactSerializesAcrossProcesses(t *testing.T) {
	if root := os.Getenv(importHelperRootEnv); root != "" {
		runImportHelper(t, root, os.Getenv(importHelperIndexEnv))
		return
	}

	root := t.TempDir()
	const workers = 4
	commands := make([]*exec.Cmd, 0, workers)
	for i := range workers {
		command := exec.Command(os.Args[0], "-test.run=^TestImportExactSerializesAcrossProcesses$")
		command.Env = append(os.Environ(),
			importHelperRootEnv+"="+root,
			importHelperIndexEnv+"="+strconv.Itoa(i),
		)
		if err := command.Start(); err != nil {
			for _, started := range commands {
				_ = started.Process.Kill()
				_ = started.Wait()
			}
			t.Fatalf("start import helper %d: %v", i, err)
		}
		commands = append(commands, command)
	}
	defer func() {
		for _, command := range commands {
			if command.ProcessState == nil {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		ready := 0
		for i := range workers {
			_, err := os.Stat(filepath.Join(root, "ready-"+strconv.Itoa(i)))
			switch {
			case err == nil:
				ready++
			case errors.Is(err, os.ErrNotExist):
			default:
				t.Fatalf("inspect helper readiness: %v", err)
			}
		}
		if ready == workers {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d import helpers became ready", ready, workers)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(root, "start"), []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("import helper %d: %v", i, err)
		}
	}

	created := 0
	identical := 0
	for i := range workers {
		data, err := os.ReadFile(filepath.Join(root, "result-"+strconv.Itoa(i)))
		if err != nil {
			t.Fatalf("read helper %d result: %v", i, err)
		}
		switch task.ImportOutcome(strings.TrimSpace(string(data))) {
		case task.ImportCreated:
			created++
		case task.ImportIdentical:
			identical++
		default:
			t.Errorf("helper %d result = %q", i, data)
		}
	}
	if created != 1 || identical != workers-1 {
		t.Fatalf("cross-process outcomes: created=%d identical=%d, want 1 and %d", created, identical, workers-1)
	}
	got, err := task.NewStore(filepath.Join(root, "tasks")).Get(exactImportTask().ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision() != exactImportTask().Revision() {
		t.Fatalf("cross-process import stored unexpected task: %+v", got)
	}
}

func runImportHelper(t *testing.T, root, index string) {
	t.Helper()
	if index == "" {
		t.Fatal("missing import helper index")
	}
	if err := os.WriteFile(filepath.Join(root, "ready-"+index), []byte("ready"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := os.Stat(filepath.Join(root, "start"))
		switch {
		case err == nil:
			goto started
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for import helper start")
		}
		time.Sleep(5 * time.Millisecond)
	}

started:
	outcome, err := task.NewStore(filepath.Join(root, "tasks")).ImportExact(exactImportTask())
	if err != nil {
		t.Fatalf("ImportExact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "result-"+index), []byte(outcome), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exactImportTask() *task.Task {
	return &task.Task{
		ID:            "repo__feat-transfer",
		Name:          "transfer task",
		Repo:          "repo",
		RepoPath:      "/src/repo",
		Branch:        "feat/transfer",
		Base:          "main",
		WorktreePath:  "/worktrees/repo-transfer",
		Mode:          task.ModeWorktree,
		State:         task.Warm,
		Owner:         "source-host",
		Next:          "continue transfer",
		Note:          "preserve everything",
		Tags:          []string{"one", "two"},
		AgentSession:  "claude:session",
		RuntimeHandle: "runtime-handle",
		RuntimeName:   "tmux",
		Created:       time.Date(2025, 2, 3, 4, 5, 6, 700, time.UTC),
		Updated:       time.Date(2025, 2, 4, 5, 6, 7, 800, time.UTC),
	}
}
