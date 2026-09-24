package prflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

type checkoutResolver struct{ detail Detail }

func (r *checkoutResolver) Detail(context.Context, Reference) (Detail, error) { return r.detail, nil }

type checkoutFixture struct {
	repository  *gittest.Repo
	remote      string
	service     *CheckoutService
	resolver    *checkoutResolver
	store       *task.Store
	experiments *experiment.Service
}

func newCheckoutFixture(t *testing.T) *checkoutFixture {
	t.Helper()
	t.Setenv("GH_HOST", "github.com")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	r := gittest.New(t)
	remote := r.WithRemote()
	base := r.Git("rev-parse", "HEAD")
	r.Branch("feature/review")
	r.Commit("pr.txt", "exact review content", "PR head")
	head := r.Git("rev-parse", "HEAD")
	r.Git("push", "origin", "HEAD:refs/pull/17/head")
	r.Git("switch", "main")
	r.Git("remote", "set-url", "origin", "https://github.com/me/project.git")
	r.Git("remote", "add", "upstream", "https://github.com/acme/project.git")
	d := Detail{Reference: Reference{Forge: forge.GitHub, Host: "github.com", Repo: "acme/project", Number: 17}, AccountID: "account-1", RepositoryID: "repository-1", HeadOID: head, BaseOID: base, BaseURL: "https://github.com/acme/project.git", HeadURL: "https://github.com/contributor/project.git", PullRequest: forge.PullRequest{Forge: forge.GitHub, Host: "github.com", Repo: "acme/project", Number: 17, HeadRepo: "contributor/project", HeadBranch: "feature/review", BaseBranch: "main", CrossRepository: true, State: forge.PRStateOpen}}
	resolver := &checkoutResolver{detail: d}
	root := t.TempDir()
	cfg := config.Default()
	cfg.Paths.WorktreeRoot = filepath.Join(root, "worktrees")
	cfg.Paths.StateDir = filepath.Join(root, "state")
	cfg.Paths.TriesRoot = filepath.Join(root, "tries")
	cfg.Paths.ProjectRoot = filepath.Join(root, "projects")
	store := task.NewStore(filepath.Join(root, "tasks"))
	cat := catalog.NewStore(filepath.Join(root, "catalog"))
	cloneRun := func(ctx context.Context, dir string, args ...string) (string, error) {
		copy := append([]string(nil), args...)
		if len(copy) > 0 && copy[0] == "clone" {
			copy[len(copy)-2] = remote
			out, err := gitx.Run(ctx, dir, copy...)
			if err == nil {
				_, err = gitx.Run(ctx, copy[len(copy)-1], "remote", "set-url", "origin", d.BaseURL)
			}
			return out, err
		}
		return gitx.Run(ctx, dir, copy...)
	}
	exps, err := experiment.NewService(experiment.ServiceConfig{Store: cat, TriesRoot: cfg.Paths.TriesRoot, ProjectRoot: cfg.Paths.ProjectRoot, Host: "test", Hooks: experiment.Hooks{GitRun: cloneRun}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, dir string, args ...string) (string, error) {
		copy := append([]string(nil), args...)
		if len(copy) > 0 && copy[0] == "fetch" {
			for i, value := range copy {
				if value == d.BaseURL {
					copy[i] = remote
				}
			}
		}
		return gitx.Run(ctx, dir, copy...)
	}
	acquire := func(ctx context.Context, request repo.AcquireRequest) (repo.AcquireResult, error) {
		request.CloneRef = remote
		result, err := repo.Acquire(ctx, request)
		if err == nil {
			_, err = gitx.Run(ctx, result.Path, "remote", "set-url", "origin", d.BaseURL)
		}
		return result, err
	}
	s := NewCheckoutService(CheckoutConfig{Config: cfg, Tasks: store, Experiments: exps, Resolver: resolver, Repositories: []string{r.Root}, Run: run, Acquire: acquire, ProvisionGuard: func(context.Context, string) error { return nil }})
	return &checkoutFixture{r, remote, s, resolver, store, exps}
}

func TestCheckoutFetchesExactForkPRThroughUpstreamAndReusesDirtyCheckout(t *testing.T) {
	f := newCheckoutFixture(t)
	req := CheckoutRequest{Reference: f.resolver.detail.Reference}
	plan, err := f.service.Plan(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plan.Path); !os.IsNotExist(err) {
		t.Fatal("preview created destination")
	}
	before := f.repository.Git("rev-parse", "HEAD")
	result, err := f.service.Apply(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Reused || f.repository.GitIn(result.Path, "rev-parse", "HEAD") != f.resolver.detail.HeadOID {
		t.Fatalf("%+v", result)
	}
	if f.repository.Git("rev-parse", "HEAD") != before {
		t.Fatal("canonical checkout changed")
	}
	if records, err := f.store.List(); err != nil || len(records) != 0 {
		t.Fatalf("created task records: %+v %v", records, err)
	}
	if err := os.WriteFile(filepath.Join(result.Path, "local.txt"), []byte("keep local edits"), 0600); err != nil {
		t.Fatal(err)
	}
	f.repository.GitIn(result.Path, "add", "local.txt")
	f.repository.GitIn(result.Path, "commit", "-m", "local follow-up")
	if err := os.WriteFile(filepath.Join(result.Path, "dirty.txt"), []byte("keep dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err = f.service.Plan(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Reused || !plan.Differs {
		t.Fatalf("reuse plan=%+v", plan)
	}
	result, err = f.service.Apply(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reused || !result.Differs || len(result.Warnings) == 0 {
		t.Fatalf("reuse=%+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(result.Path, "dirty.txt")); err != nil || string(data) != "keep dirty" {
		t.Fatal("local edits lost")
	}
}

func TestCheckoutRefusesHeadDriftAndRetainsFetchMismatch(t *testing.T) {
	t.Run("provider drift", func(t *testing.T) {
		f := newCheckoutFixture(t)
		p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference})
		if err != nil {
			t.Fatal(err)
		}
		f.resolver.detail.HeadOID = f.resolver.detail.BaseOID
		if _, err = f.service.Apply(t.Context(), p); err == nil {
			t.Fatal("head drift accepted")
		}
		if _, err = os.Stat(p.Path); !os.IsNotExist(err) {
			t.Fatal("checkout created")
		}
	})
	t.Run("fetch mismatch", func(t *testing.T) {
		f := newCheckoutFixture(t)
		p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference})
		if err != nil {
			t.Fatal(err)
		}
		f.repository.GitIn(f.remote, "update-ref", "refs/pull/17/head", f.resolver.detail.BaseOID)
		result, err := f.service.Apply(t.Context(), p)
		if err == nil || !result.PartialSuccess || len(result.Effects) != 1 || result.Effects[0].Stage != "fetch" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if _, err = os.Stat(p.Path); !os.IsNotExist(err) {
			t.Fatal("mismatched checkout created")
		}
	})
}

func TestCheckoutIgnoresUnrelatedSameBranchAndRejectsAmbiguousClone(t *testing.T) {
	f := newCheckoutFixture(t)
	f.repository.Git("switch", "feature/review") // same named branch, but origin identifies our personal fork, not the PR contributor.
	rows, err := f.service.Local(t.Context(), f.resolver.detail)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Path != "" {
		t.Fatalf("unproven branch matched: %+v", rows)
	}
	other := gittest.New(t)
	other.Git("remote", "add", "origin", f.resolver.detail.BaseURL)
	f.service.cfg.Repositories = append(f.service.cfg.Repositories, other.Root)
	_, err = f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference})
	var ambiguous *AmbiguousCheckoutError
	if !errors.As(err, &ambiguous) || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguity=%v", err)
	}
}

func TestCheckoutCreatesIndependentTryAndPreservesItOnHeadFailure(t *testing.T) {
	f := newCheckoutFixture(t)
	req := CheckoutRequest{Reference: f.resolver.detail.Reference, Try: true}
	p, err := f.service.Plan(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.service.cfg.Config.Paths.TriesRoot); !os.IsNotExist(err) {
		t.Fatal("Try preview created root")
	}
	result, err := f.service.Apply(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if result.CatalogID == "" || !result.Created || result.Path != result.RepoPath || result.HeadOID != f.resolver.detail.HeadOID {
		t.Fatalf("%+v", result)
	}
	g, err := gitx.Discover(t.Context(), result.Path)
	if err != nil || g.IsLinkedWorktree {
		t.Fatalf("Try must be standalone: %+v %v", g, err)
	}
	worktrees, err := gitx.Worktrees(t.Context(), result.Path)
	if err != nil || len(worktrees) != 1 {
		t.Fatal("Try has additional linked worktrees")
	}
	p, err = f.service.Plan(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path == result.Path {
		t.Fatal("explicit Try acquisition reused another clone")
	}
	f.repository.GitIn(f.remote, "update-ref", "refs/pull/17/head", f.resolver.detail.BaseOID)
	failed, err := f.service.Apply(t.Context(), p)
	if err == nil || !failed.PartialSuccess || failed.CatalogID == "" {
		t.Fatalf("partial Try=%+v %v", failed, err)
	}
	items, _, err := f.experiments.List(t.Context(), experiment.ListOptions{ReadOnly: true})
	if err != nil || len(items) != 2 {
		t.Fatalf("retained Tries: %d %v", len(items), err)
	}
}

func TestCheckoutProjectCloneKeepsCanonicalBaseAndDoesNotProvision(t *testing.T) {
	f := newCheckoutFixture(t)
	dest := filepath.Join(t.TempDir(), "project")
	p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference, CloneDestination: dest})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.service.Apply(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path == dest || !r.Created || r.HeadOID != f.resolver.detail.HeadOID {
		t.Fatalf("%+v", r)
	}
	if got := f.repository.GitIn(dest, "branch", "--show-current"); got != "main" {
		t.Fatalf("canonical branch=%s", got)
	}
	for _, e := range r.Effects {
		if e.Stage == "provision" {
			t.Fatal("implicit provisioning")
		}
	}
}

func TestCheckoutRejectsChangedPlanAndExistingTaskClaims(t *testing.T) {
	f := newCheckoutFixture(t)
	p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference})
	if err != nil {
		t.Fatal(err)
	}
	changed := p
	changed.Path = filepath.Join(t.TempDir(), "redirect")
	if _, err = f.service.Apply(t.Context(), changed); err == nil {
		t.Fatal("edited plan accepted")
	}
	tracked := &task.Task{Name: "existing claim", Repo: "repo", RepoPath: f.repository.Root, Branch: p.Branch, Base: "main", Mode: task.ModeWorktree, State: task.Cold}
	if err = f.store.Save(tracked); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Apply(t.Context(), p); err == nil || !strings.Contains(err.Error(), "claimed") {
		t.Fatalf("task claim accepted: %v", err)
	}
}

func TestCheckoutProvisionInitializesOnlyCommittedGitlinks(t *testing.T) {
	for _, provision := range []bool{false, true} {
		name := "default-none"
		if provision {
			name = "explicit-provision"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("GIT_ALLOW_PROTOCOL", "file")
			f := newCheckoutFixture(t)
			child := gittest.New(t)
			committed := child.Git("rev-parse", "HEAD")
			f.repository.Branch("review-submodule")
			f.repository.Git("-c", "protocol.file.allow=always", "submodule", "add", child.Root, "child")
			f.repository.Git("commit", "-am", "add committed child")
			f.repository.Git("push", f.remote, "+HEAD:refs/pull/17/head")
			f.resolver.detail.HeadOID = f.repository.Git("rev-parse", "HEAD")
			f.resolver.detail.HeadBranch = "review-submodule"
			f.repository.Git("switch", "main")
			child.Commit("later.txt", "new default branch", "advance child after gitlink")
			p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference, Provision: provision})
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.service.Apply(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			childPath := filepath.Join(result.Path, "child")
			if !provision {
				if _, err := os.Stat(filepath.Join(childPath, ".git")); !os.IsNotExist(err) {
					t.Fatal("default checkout initialized child")
				}
			} else if got := child.GitIn(childPath, "rev-parse", "HEAD"); got != committed {
				t.Fatalf("child moved from gitlink to %s", got)
			}
		})
	}
}

func TestCheckoutTryBindingSurvivesArchiveRestoreAndGraduation(t *testing.T) {
	f := newCheckoutFixture(t)
	p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference, Try: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.service.Apply(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := f.experiments.Archive(t.Context(), experiment.TransitionRequest{Ref: r.CatalogID})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Item.ID != r.CatalogID {
		t.Fatal("archive changed identity")
	}
	restored, err := f.experiments.Restore(t.Context(), experiment.TransitionRequest{Ref: r.CatalogID})
	if err != nil {
		t.Fatal(err)
	}
	graduated, err := f.experiments.Graduate(t.Context(), experiment.GraduateRequest{Ref: r.CatalogID, Name: "retained-project"})
	if err != nil {
		t.Fatal(err)
	}
	if graduated.Item.ID != restored.Item.ID {
		t.Fatal("graduation changed identity")
	}
	binding := f.repository.GitIn(graduated.Item.CurrentPath(), "config", "--local", "--get", "branch."+r.Branch+".dev-pr-url")
	if binding != f.resolver.detail.Reference.URL() {
		t.Fatalf("lost PR binding: %s", binding)
	}
}

func TestCheckoutPreservesVerifiedNativeSSHFetchEndpoint(t *testing.T) {
	f := newCheckoutFixture(t)
	ssh := "git@github.com:acme/project.git"
	f.repository.Git("remote", "set-url", "upstream", ssh)
	original := f.service.cfg.Run
	observed := ""
	f.service.cfg.Run = func(ctx context.Context, dir string, args ...string) (string, error) {
		copy := append([]string(nil), args...)
		if len(copy) > 0 && copy[0] == "fetch" {
			for i, value := range copy {
				if value == ssh {
					observed = value
					copy[i] = f.resolver.detail.BaseURL
				}
			}
		}
		return original(ctx, dir, copy...)
	}
	p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference})
	if err != nil {
		t.Fatal(err)
	}
	if p.fetchURL != ssh {
		t.Fatalf("selected endpoint=%s", p.fetchURL)
	}
	if _, err = f.service.Apply(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if observed != ssh {
		t.Fatal("checkout changed native fetch authentication transport")
	}
}

func TestCheckoutReusedProvisionRequiresGuardAndRevalidatesIdentity(t *testing.T) {
	f := newCheckoutFixture(t)
	p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.service.Apply(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	req := CheckoutRequest{Reference: f.resolver.detail.Reference, RepoPath: r.Path, Provision: true}
	f.service.cfg.ProvisionGuard = nil
	if _, err = f.service.Plan(t.Context(), req); err == nil {
		t.Fatal("unguarded provisioning was accepted")
	}
	called := false
	f.service.cfg.ProvisionGuard = func(ctx context.Context, path string) error {
		called = true
		_, err := gitx.Run(ctx, path, "switch", "-c", "changed-during-guard")
		return err
	}
	p, err = f.service.Plan(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Reused {
		t.Fatal("existing path was not selected")
	}
	r, err = f.service.Apply(t.Context(), p)
	if !called || err == nil {
		t.Fatalf("identity drift after guard accepted: %+v %v", r, err)
	}
	for _, effect := range r.Effects {
		if effect.Stage == "provision" || effect.Stage == "submodules" {
			t.Fatal("provisioning ran after guard changed checkout")
		}
	}
}

func TestCheckoutReusedExplicitProvisionDoesNotFetchOrReset(t *testing.T) {
	f := newCheckoutFixture(t)
	p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.service.Apply(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	f.repository.GitIn(r.Path, "switch", "-c", "local-review")
	f.repository.GitIn(r.Path, "config", "--local", "branch.local-review.dev-pr-url", f.resolver.detail.Reference.URL())
	f.repository.GitIn(r.Path, "commit", "--allow-empty", "-m", "local follow-up")
	before := f.repository.GitIn(r.Path, "rev-parse", "HEAD")
	guarded := ""
	f.service.cfg.ProvisionGuard = func(_ context.Context, path string) error { guarded = path; return nil }
	p, err = f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference, RepoPath: r.Path, Provision: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err = f.service.Apply(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if guarded != r.Path || !r.Reused || r.HeadOID != before {
		t.Fatalf("provisioned wrong checkout: %+v", r)
	}
	provisioned := false
	for _, effect := range r.Effects {
		if effect.Stage == "fetch" || effect.Stage == "binding" || effect.Stage == "checkout" {
			t.Fatalf("unexpected effect: %+v", effect)
		}
		provisioned = provisioned || effect.Stage == "provision"
	}
	if !provisioned {
		t.Fatal("explicit reused provisioning was skipped")
	}
}

func TestPRProvisionDetectsTargetLockfilesAndRejectsUntrustedTargetCommands(t *testing.T) {
	t.Run("target lockfiles", func(t *testing.T) {
		source := gittest.New(t)
		target := gittest.New(t)
		target.Commit("go.mod", "module example.test/review\n\ngo 1.22\n", "PR introduces Go")
		settings := wt.SettingsFor(config.Default(), source.Root)
		plan := buildPRProvisionPlan(t.Context(), settings, source.Root, target.Root)
		found := false
		for _, step := range plan.Steps {
			found = found || step.Kind == wt.StepRun && step.Ecosystem == "go"
		}
		if !found {
			t.Fatalf("PR's new ecosystem was omitted: %+v", plan)
		}
	})
	t.Run("target commands require their own trusted content", func(t *testing.T) {
		f := newCheckoutFixture(t)
		f.repository.Git("switch", "feature/review")
		f.repository.Commit(".dev-cli/config.toml", "version = 1\n[worktree]\npost_create = [\"echo unexpected\"]\n", "PR changes executable setup")
		f.repository.Git("push", f.remote, "+HEAD:refs/pull/17/head")
		f.resolver.detail.HeadOID = f.repository.Git("rev-parse", "HEAD")
		f.repository.Git("switch", "main")
		p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference, Provision: true})
		if err != nil {
			t.Fatal(err)
		}
		r, err := f.service.Apply(t.Context(), p)
		if err == nil || !strings.Contains(err.Error(), "not trusted") || !r.Created {
			t.Fatalf("untrusted PR setup result=%+v %v", r, err)
		}
		for _, effect := range r.Effects {
			if effect.Stage == "provision" {
				t.Fatal("untrusted target commands ran")
			}
		}
	})
}

func TestUnmatchedTryCannotBecomeAWorktreeHub(t *testing.T) {
	f := newCheckoutFixture(t)
	first := f.resolver.detail.Reference
	p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: first, Try: true})
	if err != nil {
		t.Fatal(err)
	}
	created, err := f.service.Apply(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	f.service.cfg.Repositories = nil
	// Exact same-PR navigation still reuses the Try's only checkout.
	p, err = f.service.Plan(t.Context(), CheckoutRequest{Reference: first})
	if err != nil || !p.Reused || p.Path != created.Path {
		t.Fatalf("same-PR Try reuse: %+v %v", p, err)
	}
	f.resolver.detail.Reference.Number = 18
	f.resolver.detail.Number = 18
	second := f.resolver.detail.Reference
	if _, err = f.service.Plan(t.Context(), CheckoutRequest{Reference: second}); !errors.Is(err, ErrNoLocalRepository) {
		t.Fatalf("unmatched Try became a repository candidate: %v", err)
	}
	if _, err = f.service.Plan(t.Context(), CheckoutRequest{Reference: second, RepoPath: created.Path}); err == nil || !strings.Contains(err.Error(), "new independent Try") {
		t.Fatalf("explicit Try hub selection: %v", err)
	}
	worktrees, err := gitx.Worktrees(t.Context(), created.Path)
	if err != nil || len(worktrees) != 1 {
		t.Fatalf("Try gained linked worktrees: %+v %v", worktrees, err)
	}
	// The excluded Try must not make a real project clone ambiguous.
	f.service.cfg.Repositories = []string{f.repository.Root}
	p, err = f.service.Plan(t.Context(), CheckoutRequest{Reference: second})
	if err != nil || p.RepoPath != f.repository.Root || p.Reused {
		t.Fatalf("ordinary project candidate lost: %+v %v", p, err)
	}
}

func TestFailedTryPRFetchCannotBecomeAWorktreeHubOnRetry(t *testing.T) {
	f := newCheckoutFixture(t)
	p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference, Try: true})
	if err != nil {
		t.Fatal(err)
	}
	f.repository.GitIn(f.remote, "update-ref", "refs/pull/17/head", f.resolver.detail.BaseOID)
	failed, err := f.service.Apply(t.Context(), p)
	if err == nil || failed.CatalogID == "" {
		t.Fatalf("expected retained failed Try: %+v %v", failed, err)
	}
	f.service.cfg.Repositories = nil
	// Restoring the provider ref does not authorize turning the retained base
	// checkout into the canonical owner of a new linked worktree.
	f.repository.GitIn(f.remote, "update-ref", "refs/pull/17/head", f.resolver.detail.HeadOID)
	if _, err = f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference}); !errors.Is(err, ErrNoLocalRepository) {
		t.Fatalf("failed Try was reused as a hub: %v", err)
	}
	if _, err = f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference, RepoPath: failed.Path}); err == nil || !strings.Contains(err.Error(), "new independent Try") {
		t.Fatalf("explicit failed Try hub selection: %v", err)
	}
	worktrees, err := gitx.Worktrees(t.Context(), failed.Path)
	if err != nil || len(worktrees) != 1 {
		t.Fatalf("failed Try gained linked worktrees: %+v %v", worktrees, err)
	}
}

func TestUncatalogedRetainedTryCannotBecomeAWorktreeHub(t *testing.T) {
	f := newCheckoutFixture(t)
	exps, err := experiment.NewService(experiment.ServiceConfig{
		Store:     catalog.NewStore(filepath.Join(t.TempDir(), "catalog")),
		TriesRoot: f.service.cfg.Config.Paths.TriesRoot, ProjectRoot: f.service.cfg.Config.Paths.ProjectRoot, Host: "test",
		Hooks: experiment.Hooks{
			CatalogCreate: func(*catalog.Entry) error { return errors.New("catalog enrollment unavailable") },
			GitRun: func(ctx context.Context, dir string, args ...string) (string, error) {
				copy := append([]string(nil), args...)
				if len(copy) > 0 && copy[0] == "clone" {
					copy[len(copy)-2] = f.remote
					out, err := gitx.Run(ctx, dir, copy...)
					if err == nil {
						_, err = gitx.Run(ctx, copy[len(copy)-1], "remote", "set-url", "origin", f.resolver.detail.BaseURL)
					}
					return out, err
				}
				return gitx.Run(ctx, dir, copy...)
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.service.cfg.Experiments = exps
	p, err := f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference, Try: true})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := f.service.Apply(t.Context(), p)
	if err == nil || !retained.Created || retained.CatalogID != "" {
		t.Fatalf("expected untracked retained clone: %+v %v", retained, err)
	}
	f.service.cfg.Repositories = nil
	if _, err = f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference}); !errors.Is(err, ErrNoLocalRepository) {
		t.Fatalf("uncataloged Try became a hub: %v", err)
	}
	if _, err = f.service.Plan(t.Context(), CheckoutRequest{Reference: f.resolver.detail.Reference, RepoPath: retained.Path}); err == nil || !strings.Contains(err.Error(), "new independent Try") {
		t.Fatalf("explicit untracked Try became a hub: %v", err)
	}
	worktrees, err := gitx.Worktrees(t.Context(), retained.Path)
	if err != nil || len(worktrees) != 1 {
		t.Fatalf("retained Try gained worktrees: %+v %v", worktrees, err)
	}
}
