package triage

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type observedEmpty struct{ runtime.None }

func (observedEmpty) Name() string                                    { return "test" }
func (observedEmpty) List(context.Context) ([]runtime.Session, error) { return nil, nil }

func fixture(t *testing.T) (*Service, *gittest.Repo) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	r := gittest.New(t)
	cfg := config.Default()
	root := t.TempDir()
	cfg.Paths.ScanRoots = []string{filepath.Dir(r.Root)}
	cfg.Paths.RepoPaths = nil
	cfg.Paths.StateDir = filepath.Join(root, "state")
	cfg.Paths.TriesRoot = filepath.Join(root, "tries")
	if e := os.MkdirAll(cfg.Paths.TriesRoot, 0755); e != nil {
		t.Fatal(e)
	}
	tasks := task.NewStore(cfg.TasksDir())
	rt := observedEmpty{}
	lc := taskflow.LifecycleConfig{Config: cfg, Tasks: tasks, Artifacts: artifact.NewStore(filepath.Join(root, "artifacts")), Host: "test", CWD: r.Root, DefaultRuntime: func() runtime.Runtime { return rt }, NamedRuntime: func(string) runtime.Runtime { return rt }}
	s := New(Config{Config: cfg, Tasks: tasks, Catalog: catalog.NewStore(cfg.AssetsDir()), Host: "test", CWD: r.Root, Runtimes: []runtime.Runtime{rt}, Lifecycle: lc})
	return s, r
}
func collect(t *testing.T, s *Service) Report {
	t.Helper()
	r, e := s.Collect(t.Context(), Options{StaleDays: 14})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func checkout(t *testing.T, r Report, path string) Item {
	t.Helper()
	if actual, e := filepath.EvalSymlinks(path); e == nil {
		path = actual
	}
	for _, i := range r.Items {
		if i.Path == path && i.Scope == "checkout" {
			return i
		}
	}
	t.Fatalf("checkout %s not found: %+v", path, r.Items)
	return Item{}
}

func TestTriageReadOnlyCollectsBranchesWorktreesAndTries(t *testing.T) {
	s, r := fixture(t)
	r.Git("branch", "forgotten")
	wt := filepath.Join(t.TempDir(), "linked")
	r.Git("worktree", "add", "-b", "work", wt)
	r.Write("uncommitted", "work")
	plain := filepath.Join(s.cfg.Config.Paths.TriesRoot, "scratch")
	if e := os.MkdirAll(plain, 0755); e != nil {
		t.Fatal(e)
	}
	gitTry := filepath.Join(s.cfg.Config.Paths.TriesRoot, "git-try")
	r.Git("clone", r.Root, gitTry)
	plain, _ = filepath.EvalSymlinks(plain)
	gitTry, _ = filepath.EvalSymlinks(gitTry)
	report := collect(t, s)
	foundBranch, foundPlain, foundGitTry := false, false, false
	for _, i := range report.Items {
		if i.Branch != nil && i.Branch.Ref == "refs/heads/forgotten" && i.Scope == "branch" {
			foundBranch = true
		}
		if i.Path == plain && i.Kind == "try" && i.Scope == "directory" {
			foundPlain = true
		}
		if i.Path == gitTry && i.Kind == "try" && i.Scope == "checkout" {
			foundGitTry = true
		}
	}
	if !foundBranch || !foundPlain || !foundGitTry {
		t.Fatalf("coverage branch=%v plain=%v gitTry=%v", foundBranch, foundPlain, foundGitTry)
	}
	_ = checkout(t, report, wt)
	if _, e := os.Stat(s.cfg.Config.StateDir()); !os.IsNotExist(e) {
		t.Fatalf("read-only scan wrote state: %v", e)
	}
	if r.Git("status", "--porcelain") == "" {
		t.Fatal("scan altered uncommitted work")
	}
}

func TestTriageDeferredWorkReappearsOnSameSizeEdit(t *testing.T) {
	s, r := fixture(t)
	r.Write("pending", "first")
	i := checkout(t, collect(t, s), r.Root)
	if e := s.Store.SetIntent(t.Context(), i, "local", time.Time{}); e != nil {
		t.Fatal(e)
	}
	i = checkout(t, collect(t, s), r.Root)
	if i.Deferred != "local" {
		t.Fatalf("intent=%q findings=%+v", i.Deferred, i.Findings)
	}
	r.Write("pending", "other")
	i = checkout(t, collect(t, s), r.Root)
	if i.Deferred != "" {
		t.Fatal("new contents hidden by prior intent")
	}
	if e := s.Store.SetIntent(t.Context(), i, "snooze", time.Now().Add(-time.Hour)); e != nil {
		t.Fatal(e)
	}
	if checkout(t, collect(t, s), r.Root).Deferred != "" {
		t.Fatal("expired snooze remains hidden")
	}
}

func TestTriageIgnoredCleanupRevalidatesAndPreservesBranch(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("protected cleanup requires stable filesystem identity")
	}
	s, r := fixture(t)
	r.Commit(".gitignore", "cache/\n.env\n", "ignore")
	wt := filepath.Join(t.TempDir(), "linked")
	r.Git("worktree", "add", "-b", "work", wt)
	if e := os.MkdirAll(filepath.Join(wt, "cache"), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(wt, "cache", "data"), []byte("rebuild"), 0600); e != nil {
		t.Fatal(e)
	}
	i := checkout(t, collect(t, s), wt)
	b, e := s.Prepare(t.Context(), []Item{i}, "remove-checkout")
	if e != nil {
		t.Fatal(e)
	}
	if b.ReadyCount() != 0 {
		t.Fatal("undeclared ignored content allowed")
	}
	if e = s.Store.SetDisposable(t.Context(), i.RepositoryID, []string{"cache"}); e != nil {
		t.Fatal(e)
	}
	i = checkout(t, collect(t, s), wt)
	b, e = s.Prepare(t.Context(), []Item{i}, "remove-checkout")
	if e != nil {
		t.Fatal(e)
	}
	if b.ReadyCount() != 1 {
		t.Fatalf("blocked=%+v", b.Previews())
	}
	if _, e = s.Apply(t.Context(), b, b.ID, "wrong", nil); e == nil {
		t.Fatal("wrong cleanup token accepted")
	}
	if e = os.WriteFile(filepath.Join(wt, ".env"), []byte("retain"), 0600); e != nil {
		t.Fatal(e)
	}
	l, e := s.Apply(t.Context(), b, b.ID, b.Token, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(l.Outcomes) != 1 || l.Outcomes[0].Status == "completed" {
		t.Fatalf("stale cleanup=%+v", l)
	}
	if _, e = os.Stat(filepath.Join(wt, ".env")); e != nil {
		t.Fatal("new ignored data removed")
	}
	if e = os.Remove(filepath.Join(wt, ".env")); e != nil {
		t.Fatal(e)
	}
	i = checkout(t, collect(t, s), wt)
	b, e = s.Prepare(t.Context(), []Item{i}, "remove-checkout")
	if e != nil {
		t.Fatal(e)
	}
	oid := r.Git("rev-parse", "refs/heads/work")
	l, e = s.Apply(t.Context(), b, b.ID, b.Token, nil)
	if e != nil {
		t.Fatal(e)
	}
	if l.Outcomes[0].Status != "completed" {
		t.Fatalf("cleanup=%+v", l)
	}
	if _, e = os.Stat(wt); !os.IsNotExist(e) {
		t.Fatal("checkout not removed")
	}
	if r.Git("rev-parse", "refs/heads/work") != oid {
		t.Fatal("local branch changed")
	}
}

func TestTriageBatchDedupCancellationAndReceipts(t *testing.T) {
	s, r := fixture(t)
	r.WithRemote()
	r.Git("remote", "add", "second", r.Git("remote", "get-url", "origin"))
	var item Item
	for _, i := range collect(t, s).Items {
		if i.Scope == "repository" {
			item = i
			break
		}
	}
	b, e := s.Prepare(t.Context(), []Item{item, item}, "fetch")
	if e != nil {
		t.Fatal(e)
	}
	if len(b.Previews()) != 2 {
		t.Fatalf("duplicate operations=%+v", b.Previews())
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l, e := s.Apply(ctx, b, b.ID, b.Token, func(Outcome) { cancel() })
	if e != nil {
		t.Fatal(e)
	}
	if len(l.Outcomes) != 2 || l.Outcomes[0].Status != "completed" || l.Outcomes[1].Status != "canceled" {
		t.Fatalf("outcomes=%+v", l.Outcomes)
	}
	if _, e = os.Stat(l.Path); e != nil {
		t.Fatal(e)
	}
}

func TestTriageNonGitTryDeferralDetectsFileEdits(t *testing.T) {
	s, _ := fixture(t)
	dir := filepath.Join(s.cfg.Config.Paths.TriesRoot, "scratch")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "thought.txt")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	find := func() Item {
		for _, i := range collect(t, s).Items {
			if i.Kind == "try" && i.Scope == "directory" {
				return i
			}
		}
		t.Fatal("Try not observed")
		return Item{}
	}
	i := find()
	if err := s.Store.SetIntent(t.Context(), i, "local", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if find().Deferred != "local" {
		t.Fatal("non-Git Try cannot be deferred")
	}
	if err := os.WriteFile(path, []byte("later"), 0600); err != nil {
		t.Fatal(err)
	}
	if find().Deferred != "" {
		t.Fatal("non-Git file edit was hidden")
	}
}
