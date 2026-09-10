package taskflow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type emptyRuntime struct{ runtime.None }

func (emptyRuntime) Name() string                                    { return "test" }
func (emptyRuntime) List(context.Context) ([]runtime.Session, error) { return nil, nil }

type changingRuntime struct {
	emptyRuntime
	path     string
	occupied bool
}

func (r *changingRuntime) AgentActivities(context.Context) ([]runtime.AgentActivity, error) {
	if r.occupied {
		return []runtime.AgentActivity{{Agent: "test", Status: "idle", CWD: r.path}}, nil
	}
	return nil, nil
}

func syncPlan(t *testing.T, r *gittest.Repo, action taskflow.Action) (*taskflow.Service, taskflow.Plan) {
	t.Helper()
	g, e := gitx.Discover(t.Context(), r.Root)
	if e != nil {
		t.Fatal(e)
	}
	common, e := pathx.Canonical(g.GitCommonDir)
	if e != nil {
		t.Fatal(e)
	}
	s, e := taskflow.NewSyncService(taskflow.SyncConfig{Tasks: task.NewStore(filepath.Join(t.TempDir(), "tasks")), Host: "test", Runtimes: func() []runtime.Runtime { return []runtime.Runtime{emptyRuntime{}} }})
	if e != nil {
		t.Fatal(e)
	}
	l := taskflow.Locator{RepoPath: r.Root, GitCommonDir: common, RepositoryID: common, RowKind: "checkout", RowKey: r.Root, CheckoutPath: r.Root, Branch: "main", HeadOID: r.Git("rev-parse", "HEAD"), Remote: "origin"}
	req, e := taskflow.NewRequest(l, taskflow.SyncOptions{Operation: action})
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.Plan(t.Context(), req)
	if e != nil {
		t.Fatal(e)
	}
	return s, p
}

func TestTriageSyncPushExactRefAndRejectStale(t *testing.T) {
	r := gittest.New(t)
	remote := r.WithRemote()
	r.Commit("change", "new", "commit")
	r.Git("tag", "-a", "private-tag", "-m", "private")
	r.Git("config", "push.followTags", "true")
	s, p := syncPlan(t, r, taskflow.PushBranch)
	if p.Availability != taskflow.AvailabilityReady {
		t.Fatalf("plan=%+v", p.Conditions())
	}
	if _, e := s.Apply(t.Context(), p, taskflow.Approve("wrong")); e == nil {
		t.Fatal("wrong approval accepted")
	}
	if _, e := s.Apply(t.Context(), p, taskflow.Approve(p.PlanID)); e != nil {
		t.Fatal(e)
	}
	if got := r.GitIn(remote, "for-each-ref", "--format=%(refname)", "refs/tags"); got != "" {
		t.Fatalf("unexpected pushed tags %s", got)
	}
	r.Commit("change", "next", "next")
	s, p = syncPlan(t, r, taskflow.PushBranch)
	r.Commit("change", "later", "later")
	_, e := s.Apply(t.Context(), p, taskflow.Approve(p.PlanID))
	if !errors.Is(e, taskflow.ErrStalePlan) {
		t.Fatalf("stale error=%v", e)
	}
}

func TestSyncFailureKeepsSafeDiagnosticAndRemoteRef(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX fixture hook")
	}
	r := gittest.New(t)
	remote := r.WithRemote()
	before := r.GitIn(remote, "rev-parse", "refs/heads/main")
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	r.GitIn(remote, "config", "core.hooksPath", hooks)
	if err := os.WriteFile(filepath.Join(hooks, "pre-receive"), []byte("#!/bin/sh\necho 'remote policy: token=LOCAL_TEST_SECRET' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r.Commit("change", "new", "commit")
	s, p := syncPlan(t, r, taskflow.PushBranch)
	result, err := s.Apply(t.Context(), p, taskflow.Approve(p.PlanID))
	if err == nil {
		t.Fatal("rejection reported success")
	}
	steps := result.AttemptedSteps()
	if len(steps) != 1 || steps[0].Diagnostic == nil || steps[0].Diagnostic.Code != "remote-policy" {
		t.Fatalf("%+v", steps)
	}
	if strings.Contains(steps[0].Diagnostic.Details, "LOCAL_TEST_SECRET") {
		t.Fatal("credential leaked")
	}
	if !strings.Contains(steps[0].Diagnostic.Details, "hook declined") {
		t.Fatal("porcelain rejection lost")
	}
	steps[0].Diagnostic.Summary = "modified by caller"
	if result.AttemptedSteps()[0].Diagnostic.Summary == "modified by caller" {
		t.Fatal("mutable diagnostic escaped result")
	}
	if r.GitIn(remote, "rev-parse", "refs/heads/main") != before {
		t.Fatal("rejected push changed remote")
	}
}

func TestTriageFastForwardPreservesIgnoredCollision(t *testing.T) {
	r := gittest.New(t)
	r.WithRemote()
	r.Commit(".gitignore", "local-secret\n", "ignore")
	r.Git("push", "origin", "main")
	base := r.Git("rev-parse", "HEAD")
	r.Write("local-secret", "remote version")
	r.Git("add", "-f", "local-secret")
	r.Git("commit", "-m", "remote adds file")
	r.Git("push", "origin", "main")
	r.Git("reset", "--hard", base)
	r.Write("local-secret", "retain my bytes")
	s, p := syncPlan(t, r, taskflow.FastForwardBranch)
	if p.Availability != taskflow.AvailabilityReady {
		t.Fatalf("plan=%+v", p.Conditions())
	}
	if _, e := s.Apply(t.Context(), p, taskflow.Approve(p.PlanID)); e == nil {
		t.Fatal("ignored collision did not abort")
	}
	data, e := os.ReadFile(filepath.Join(r.Root, "local-secret"))
	if e != nil || string(data) != "retain my bytes" {
		t.Fatal("ignored contents overwritten")
	}
	if r.Git("rev-parse", "HEAD") != base {
		t.Fatal("branch moved after failed fast-forward")
	}
}

func TestTriageSyncFetchDoesNotTouchLocalHeads(t *testing.T) {
	r := gittest.New(t)
	r.WithRemote()
	head := r.Git("rev-parse", "HEAD")
	r.Commit("remote", "remote", "remote")
	r.Git("push", "origin", "main")
	r.Git("reset", "--hard", head)
	r.Git("update-ref", "refs/remotes/origin/main", head)
	s, p := syncPlan(t, r, taskflow.FetchRepository)
	if _, e := s.Apply(t.Context(), p, taskflow.Approve(p.PlanID)); e != nil {
		t.Fatal(e)
	}
	if r.Git("rev-parse", "HEAD") != head {
		t.Fatal("fetch changed local head")
	}
	if r.Git("rev-parse", "refs/remotes/origin/main") == head {
		t.Fatal("fetch did not refresh refs")
	}
}

func TestTriageNewIdleAgentInvalidatesPush(t *testing.T) {
	r := gittest.New(t)
	remote := r.WithRemote()
	before := r.GitIn(remote, "rev-parse", "refs/heads/main")
	r.Commit("work", "committed", "new work")
	_, template := syncPlan(t, r, taskflow.PushBranch)
	rt := &changingRuntime{path: r.Root}
	s, err := taskflow.NewSyncService(taskflow.SyncConfig{
		Tasks: task.NewStore(filepath.Join(t.TempDir(), "tasks")), Host: "test",
		Runtimes: func() []runtime.Runtime { return []runtime.Runtime{rt} },
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Plan(t.Context(), template.Request)
	if err != nil {
		t.Fatal(err)
	}
	if p.Availability != taskflow.AvailabilityReady {
		t.Fatal(p.Conditions())
	}
	rt.occupied = true
	if _, err = s.Apply(t.Context(), p, taskflow.Approve(p.PlanID)); !errors.Is(err, taskflow.ErrStalePlan) {
		t.Fatalf("new agent was not rejected: %v", err)
	}
	if r.GitIn(remote, "rev-parse", "refs/heads/main") != before {
		t.Fatal("published while an agent occupied the checkout")
	}
}
