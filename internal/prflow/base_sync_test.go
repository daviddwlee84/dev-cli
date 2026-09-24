package prflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type baseSyncRuntime struct{ runtime.None }

func (baseSyncRuntime) Name() string { return "fixture" }

func mergedSyncFixture(t *testing.T) (*checkoutFixture, *BaseSyncService, BaseSyncRequest) {
	t.Helper()
	f := newCheckoutFixture(t)
	f.repository.Branch("integration")
	f.repository.Commit("merged.txt", "merged change", "squash merge")
	mergeOID := f.repository.Git("rev-parse", "HEAD")
	f.repository.Git("push", f.remote, "HEAD:refs/heads/main")
	f.repository.Git("switch", "main")
	f.repository.Git("config", "branch.main.remote", "upstream")
	f.repository.Git("config", "branch.main.merge", "refs/heads/main")
	f.repository.Git("update-ref", "refs/remotes/upstream/main", f.resolver.detail.BaseOID)
	f.resolver.detail.State = forge.PRStateMerged
	f.resolver.detail.MergeOID = mergeOID
	f.resolver.detail.BaseOID = mergeOID
	s, err := NewBaseSyncService(BaseSyncConfig{Tasks: f.store, Host: "test", Runtimes: func() []runtime.Runtime { return []runtime.Runtime{baseSyncRuntime{}} }, Resolver: f.resolver, Run: taskflow.GitRunFunc(f.service.cfg.Run)})
	if err != nil {
		t.Fatal(err)
	}
	return f, s, BaseSyncRequest{Reference: f.resolver.detail.Reference, RepoPath: f.repository.Root}
}

func fetchSyncBase(t *testing.T, s *BaseSyncService, req BaseSyncRequest) {
	t.Helper()
	plan, err := s.PlanFetch(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyFetch(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
}

func TestBaseSyncFetchThenGuardedFastForwardAndNoOp(t *testing.T) {
	f, s, req := mergedSyncFixture(t)
	before := f.repository.Git("rev-parse", "HEAD")
	if _, err := s.Plan(t.Context(), req); err == nil {
		t.Fatal("stale cached upstream accepted without merge proof")
	}
	fetchSyncBase(t, s, req)
	if f.repository.Git("rev-parse", "HEAD") != before {
		t.Fatal("fetch changed local branch")
	}
	p, err := s.Plan(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.NoOp || p.Plan.Availability != taskflow.AvailabilityReady {
		t.Fatalf("plan=%+v conditions=%+v", p, p.Plan.Conditions())
	}
	r, err := s.Apply(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if r.NewOID != f.resolver.detail.MergeOID || r.PartialSuccess {
		t.Fatalf("result=%+v", r)
	}
	p, err = s.Plan(t.Context(), req)
	if err != nil || !p.NoOp {
		t.Fatalf("no-op=%+v %v", p, err)
	}
	if _, err = s.Apply(t.Context(), p); err != nil {
		t.Fatal(err)
	}
}

func TestBaseSyncRejectsUnconfirmedMergeAndForkTracking(t *testing.T) {
	f, s, req := mergedSyncFixture(t)
	f.resolver.detail.State = forge.PRStateOpen
	if _, err := s.PlanFetch(t.Context(), req); err == nil {
		t.Fatal("unconfirmed merge accepted")
	}
	f.resolver.detail.State = forge.PRStateMerged
	f.repository.Git("config", "branch.main.remote", "origin")
	if _, err := s.PlanFetch(t.Context(), req); err == nil {
		t.Fatal("personal fork accepted as base repository")
	}
}

func TestBaseSyncExplicitRebaseRetainsSiblingRefs(t *testing.T) {
	f, s, req := mergedSyncFixture(t)
	f.repository.Commit("local.txt", "local change", "local commit")
	old := f.repository.Git("rev-parse", "HEAD")
	f.repository.Git("branch", "keep-local", old)
	f.repository.Git("config", "rebase.updateRefs", "true")
	f.repository.Git("config", "rebase.autoStash", "true")
	fetchSyncBase(t, s, req)
	if _, err := s.Plan(t.Context(), req); err == nil || !strings.Contains(err.Error(), "explicitly choose rebase") {
		t.Fatalf("ff accepted local commits: %v", err)
	}
	req.Mode = "rebase"
	p, err := s.Plan(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Plan.Availability != taskflow.AvailabilityReady {
		t.Fatalf("conditions=%+v", p.Plan.Conditions())
	}
	r, err := s.Apply(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if r.NewOID == old || f.repository.Git("rev-parse", "keep-local") != old {
		t.Fatalf("sibling ref moved: %+v", r)
	}
	if f.repository.Git("show", "HEAD:local.txt") != "local change" {
		t.Fatal("local commit lost")
	}
	if f.repository.Git("rev-parse", "HEAD^") != f.resolver.detail.MergeOID {
		t.Fatal("rebase used wrong target")
	}
}

func TestBaseSyncDirtyBlocksRebaseAndConflictRetainsRecovery(t *testing.T) {
	t.Run("dirty", func(t *testing.T) {
		f, s, req := mergedSyncFixture(t)
		f.repository.Commit("local.txt", "local", "local")
		fetchSyncBase(t, s, req)
		f.repository.Write("dirty", "keep")
		req.Mode = "rebase"
		p, err := s.Plan(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if p.Plan.Availability == taskflow.AvailabilityReady {
			t.Fatal("dirty rebase became ready")
		}
		if _, err = s.Apply(t.Context(), p); err == nil {
			t.Fatal("dirty rebase applied")
		}
		if _, err = os.Stat(filepath.Join(f.repository.Root, "dirty")); err != nil {
			t.Fatal("dirty work lost")
		}
	})
	t.Run("conflict", func(t *testing.T) {
		f, s, req := mergedSyncFixture(t)
		f.repository.Commit("merged.txt", "local version", "local competing commit")
		old := f.repository.Git("rev-parse", "HEAD")
		fetchSyncBase(t, s, req)
		req.Mode = "rebase"
		p, err := s.Plan(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.Apply(t.Context(), p)
		if err == nil || !r.PartialSuccess || len(r.Recovery) == 0 || !strings.Contains(strings.Join(r.Recovery, " "), old) {
			t.Fatalf("conflict result=%+v %v", r, err)
		}
	})
}

func TestBaseSyncDoesNotSwitchCanonicalBranch(t *testing.T) {
	f, s, req := mergedSyncFixture(t)
	fetchSyncBase(t, s, req)
	f.repository.Git("switch", "feature/review")
	if _, err := s.Plan(t.Context(), req); err == nil || !strings.Contains(err.Error(), "not checked out") {
		t.Fatalf("unexpected plan: %v", err)
	}
	if f.repository.Git("branch", "--show-current") != "feature/review" {
		t.Fatal("canonical branch switched")
	}
}

func TestBaseSyncPlanDoesNotSerializeRemoteCredentials(t *testing.T) {
	f, s, req := mergedSyncFixture(t)
	f.repository.Git("remote", "set-url", "upstream", "https://fixture-user:fixture-password@github.com/acme/project.git")
	p, err := s.PlanFetch(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "fixture-password") || strings.Contains(string(encoded), "fixture-user") {
		t.Fatal("plan serialized remote credentials")
	}
}
