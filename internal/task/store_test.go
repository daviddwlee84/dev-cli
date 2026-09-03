package task_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func TestRecordRevisionUsesExactPersistedBytesAndTaskID(t *testing.T) {
	dir := t.TempDir()
	s := task.NewStore(dir)
	tk := newTask("repo", "feat/exact-bytes", task.Hot)
	if err := s.Save(tk); err != nil {
		t.Fatal(err)
	}

	record, err := s.GetRecord(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, tk.ID+".toml"))
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte(tk.ID+"\x00"), data...)
	want := fmt.Sprintf("%x", sha256.Sum256(payload))
	if record.Revision != want {
		t.Fatalf("revision = %q, want exact byte hash %q", record.Revision, want)
	}
	if strings.Contains(string(data), "\nrevision =") {
		t.Fatal("ephemeral revision must not be persisted in task TOML")
	}

	if err := os.WriteFile(filepath.Join(dir, "other-id.toml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	other, err := s.GetRecord("other-id")
	if err != nil {
		t.Fatal(err)
	}
	if other.Revision == record.Revision {
		t.Fatal("identical bytes under different task IDs must have different revisions")
	}

	if err := os.WriteFile(filepath.Join(dir, tk.ID+".toml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	same, err := s.GetRecord(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if same.Revision != record.Revision {
		t.Fatal("a byte-for-byte rewrite must retain its revision")
	}

	changedBytes := append(append([]byte(nil), data...), '\n')
	if err := os.WriteFile(filepath.Join(dir, tk.ID+".toml"), changedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := s.GetRecord(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Revision == record.Revision {
		t.Fatal("a representation-only byte change must change the revision")
	}
	if !changed.Task.Updated.Equal(record.Task.Updated) {
		t.Fatal("the representation-only change should not rely on Updated")
	}
}

func TestSaveRevisionChangesWithinSameSecond(t *testing.T) {
	s := task.NewStore(t.TempDir())
	tk := newTask("repo", "feat/same-second", task.Hot)

	for attempt := 0; attempt < 10; attempt++ {
		tk.Next = fmt.Sprintf("before-%d", attempt)
		if err := s.Save(tk); err != nil {
			t.Fatal(err)
		}
		before, err := s.GetRecord(tk.ID)
		if err != nil {
			t.Fatal(err)
		}

		tk.Next = fmt.Sprintf("after-%d", attempt)
		if err := s.Save(tk); err != nil {
			t.Fatal(err)
		}
		after, err := s.GetRecord(tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if before.Task.Updated.Equal(after.Task.Updated) {
			if before.Revision == after.Revision {
				t.Fatal("same-second content writes must receive different revisions")
			}
			return
		}
	}
	t.Fatal("could not observe two writes within one timestamp second")
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
	info, err := os.Stat(filepath.Join(dir, in.ID+".toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("task file mode = %04o, want 0644", got)
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

func TestCreateRejectsSanitizedTaskIDCollision(t *testing.T) {
	s := task.NewStore(t.TempDir())
	first := newTask("repo", "feat/a", task.Hot)
	created, err := s.Create(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision == "" {
		t.Fatal("Create must return the persisted revision")
	}

	second := newTask("repo", "feat-a", task.Warm)
	if task.MakeID(first.Repo, first.Branch) != task.MakeID(second.Repo, second.Branch) {
		t.Fatal("test branches must exercise a sanitized ID collision")
	}
	if _, err := s.Create(context.Background(), second); !errors.Is(err, task.ErrAlreadyExists) {
		t.Fatalf("colliding Create = %v, want ErrAlreadyExists", err)
	}
	stored, err := s.Get(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Branch != first.Branch || stored.State != first.State {
		t.Fatalf("collision overwrote first task: %+v", stored)
	}
}

func TestUpdateAndDeleteRejectStaleRevision(t *testing.T) {
	s := task.NewStore(t.TempDir())
	created, err := s.Create(context.Background(), newTask("repo", "feat/revision", task.Hot))
	if err != nil {
		t.Fatal(err)
	}

	winner := created.Task
	winner.Next = "winner"
	updated, err := s.Update(context.Background(), &winner, created.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision == created.Revision {
		t.Fatal("successful content update must return a new revision")
	}

	stale := created.Task
	stale.Next = "stale overwrite"
	_, err = s.Update(context.Background(), &stale, created.Revision)
	if !errors.Is(err, task.ErrStaleRevision) {
		t.Fatalf("stale Update = %v, want ErrStaleRevision", err)
	}
	var staleErr *task.StaleRevisionError
	if !errors.As(err, &staleErr) {
		t.Fatalf("stale Update error type = %T, want *StaleRevisionError", err)
	}
	if staleErr.ID != created.Task.ID || staleErr.Expected != created.Revision || staleErr.Actual != updated.Revision {
		t.Errorf("stale revision detail = %+v", staleErr)
	}
	stored, err := s.Get(created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Next != "winner" {
		t.Fatalf("stale update changed persisted task to %q", stored.Next)
	}

	if err := s.DeleteIfRevision(context.Background(), created.Task.ID, created.Revision); !errors.Is(err, task.ErrStaleRevision) {
		t.Fatalf("stale DeleteIfRevision = %v, want ErrStaleRevision", err)
	}
	if _, err := s.Get(created.Task.ID); err != nil {
		t.Fatalf("stale delete removed the task: %v", err)
	}
	if err := s.DeleteIfRevision(context.Background(), created.Task.ID, updated.Revision); err != nil {
		t.Fatalf("current DeleteIfRevision: %v", err)
	}
	if _, err := s.Get(created.Task.ID); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("deleted task lookup = %v, want ErrNotFound", err)
	}
}

func TestCreateHonorsLockCancellation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tasks")
	s := task.NewStore(dir)
	entered := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- lockx.WithDir(context.Background(), dir, "task test holder", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, transactionErr := s.Create(ctx, newTask("repo", "feat/canceled", task.Hot))
	cancel()
	close(release)
	holderErr := <-holderDone

	if holderErr != nil {
		t.Fatalf("lock holder: %v", holderErr)
	}
	if !errors.Is(transactionErr, context.DeadlineExceeded) {
		t.Fatalf("contended Create = %v, want context deadline", transactionErr)
	}
	if _, err := s.Get(task.MakeID("repo", "feat/canceled")); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("canceled transaction persisted a task: %v", err)
	}
}

func TestWithLockSerializesCompatibilityWriters(t *testing.T) {
	s := task.NewStore(t.TempDir())
	created, err := s.Create(context.Background(), newTask("repo", "feat/transaction", task.Hot))
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	transactionDone := make(chan error, 1)
	go func() {
		transactionDone <- s.WithLock(context.Background(), func(tx *task.Tx) error {
			current, err := tx.GetRecord(created.Task.ID)
			if err != nil {
				return err
			}
			close(entered)
			<-release
			updated := current.Task
			updated.Next = "transaction"
			_, err = tx.Update(&updated, current.Revision)
			return err
		})
	}()
	<-entered

	compatibilityDone := make(chan error, 1)
	compatibility := created.Task
	compatibility.Next = "compatibility"
	go func() { compatibilityDone <- s.Save(&compatibility) }()
	select {
	case err := <-compatibilityDone:
		t.Fatalf("compatibility Save escaped the transaction lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-transactionDone; err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if err := <-compatibilityDone; err != nil {
		t.Fatalf("compatibility Save after transaction: %v", err)
	}
}

func TestTxListRecordsRetainsDiagnosticsUnderLock(t *testing.T) {
	dir := t.TempDir()
	s := task.NewStore(dir)
	tracked := newTask("repo", "feat/claim", task.Done)
	if err := s.Save(tracked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "corrupt.toml"), []byte("not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.WithLock(context.Background(), func(tx *task.Tx) error {
		records, diagnostics, err := tx.ListRecords()
		if err != nil {
			return err
		}
		if len(records) != 1 || records[0].Task.ID != tracked.ID || records[0].Revision == "" {
			t.Fatalf("transaction records = %+v", records)
		}
		if len(diagnostics) != 1 || diagnostics[0].ID != "corrupt" || diagnostics[0].Err == nil {
			t.Fatalf("transaction diagnostics = %+v", diagnostics)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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
	if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("this is not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	records, diagnostics, err := s.ListRecords()
	if err != nil {
		t.Fatalf("one corrupt file must not fail the record listing: %v", err)
	}
	if len(records) != 1 || records[0].Task.Repo != "good" {
		t.Fatalf("valid records = %+v", records)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want one corrupt record", diagnostics)
	}
	if diagnostics[0].ID != "broken" || filepath.Base(diagnostics[0].Path) != "broken.toml" {
		t.Errorf("diagnostic identity = %+v", diagnostics[0])
	}
	if diagnostics[0].Err == nil || !strings.Contains(diagnostics[0].Error(), "broken.toml") {
		t.Errorf("diagnostic did not retain the read failure: %+v", diagnostics[0])
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("compatibility List must still skip corruption: %v", err)
	}
	if len(got) != 1 || got[0].Repo != "good" {
		t.Errorf("compatibility List = %+v", got)
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
