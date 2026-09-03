package taskflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestLifecycleLocateTaskBuildsExactModeLocators(t *testing.T) {
	tests := []struct {
		name  string
		mode  task.CheckoutMode
		state task.State
	}{
		{name: "worktree", mode: task.ModeWorktree, state: task.Hot},
		{name: "branch", mode: task.ModeBranch, state: task.Warm},
		{name: "direct", mode: task.ModeDirect, state: task.Warm},
		{name: "cold worktree without checkout", mode: task.ModeWorktree, state: task.Cold},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLifecycleGitFixture(t, test.mode, test.state)
			locator, err := fixture.service.LocateTask(context.Background(), fixture.record.Task.ID)
			if err != nil {
				t.Fatalf("LocateTask: %v", err)
			}
			record, err := fixture.tasks.GetRecord(fixture.record.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			repository, err := gitx.Discover(context.Background(), fixture.repo)
			if err != nil {
				t.Fatal(err)
			}
			wantRepo, _ := pathx.Canonical(repository.MainRoot)
			wantCommon, _ := pathx.Canonical(repository.GitCommonDir)
			wantCheckout := wantRepo
			if test.mode == task.ModeWorktree {
				wantCheckout = ""
				if record.Task.WorktreePath != "" {
					wantCheckout, _ = pathx.Canonical(record.Task.WorktreePath)
				}
			}
			if locator.TaskID != record.Task.ID || locator.TaskRevision != record.Revision ||
				locator.RepoPath != wantRepo || locator.GitCommonDir != wantCommon ||
				locator.RepoKey != wantCommon || locator.RepositoryID != wantCommon ||
				locator.CheckoutPath != wantCheckout || locator.Branch != record.Task.Branch ||
				locator.Base != record.Task.Base || locator.Mode != test.mode || locator.State != test.state {
				t.Fatalf("locator = %+v\nrecord = %+v", locator, record)
			}
			if locator.Remote != "origin" || locator.Upstream == "" {
				t.Fatalf("remote/upstream = %q/%q", locator.Remote, locator.Upstream)
			}
			for name, oid := range map[string]string{
				"head": locator.HeadOID, "base": locator.BaseOID, "upstream": locator.UpstreamOID,
			} {
				if !fullGitOID.MatchString(oid) {
					t.Errorf("%s OID = %q, want a full OID", name, oid)
				}
			}
			if wantCheckout == "" {
				if locator.RowKind != "task" || locator.RowKey != record.Task.ID {
					t.Fatalf("checkout-free row = %q/%q", locator.RowKind, locator.RowKey)
				}
			} else if locator.RowKind != "checkout" || locator.RowKey != wantCheckout {
				t.Fatalf("checkout row = %q/%q, want checkout/%q", locator.RowKind, locator.RowKey, wantCheckout)
			}
		})
	}
}

func TestLifecycleLocateTaskRetainsMissingAndStaleCheckoutEvidence(t *testing.T) {
	t.Run("missing recorded checkout", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Warm)
		missing := filepath.Join(fixture.root, "missing-checkout")
		updated := updateFixtureTask(t, fixture, func(candidate *task.Task) {
			candidate.WorktreePath = missing
		})
		locator, err := fixture.service.LocateTask(context.Background(), updated.Task.ID)
		if err != nil {
			t.Fatalf("LocateTask: %v", err)
		}
		want, _ := pathx.Canonical(missing)
		if locator.CheckoutPath != want || locator.RowKind != "task" || locator.RowKey != updated.Task.ID {
			t.Fatalf("missing locator = %+v, want expected checkout %q", locator, want)
		}
		if !fullGitOID.MatchString(locator.HeadOID) || !fullGitOID.MatchString(locator.BaseOID) {
			t.Fatalf("missing checkout lost available refs: %+v", locator)
		}
		request, err := NewRequest(locator, ParkWarmOptions{})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := fixture.service.Plan(context.Background(), request)
		if err != nil {
			t.Fatalf("Plan should retain missing checkout as blockers: %v", err)
		}
		if plan.Availability == AvailabilityReady {
			t.Fatalf("missing checkout unexpectedly ready: %+v", plan.Conditions())
		}
	})

	t.Run("stale registered checkout", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Warm)
		checkout, _ := pathx.Canonical(fixture.record.Task.WorktreePath)
		if err := os.RemoveAll(checkout); err != nil {
			t.Fatal(err)
		}
		locator, err := fixture.service.LocateTask(context.Background(), fixture.record.Task.ID)
		if err != nil {
			t.Fatalf("LocateTask: %v", err)
		}
		if locator.CheckoutPath != checkout || locator.RowKind != "checkout" || locator.RowKey != checkout {
			t.Fatalf("stale registered locator = %+v", locator)
		}
		if !fullGitOID.MatchString(locator.HeadOID) {
			t.Fatalf("stale registered HEAD = %q", locator.HeadOID)
		}
	})
}

func TestLifecycleLocateTaskCanonicalizesPersistedAliases(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeWorktree, task.Hot)
	repoAlias := filepath.Join(fixture.root, "repo-alias")
	checkoutAlias := filepath.Join(fixture.root, "checkout-alias")
	if err := os.Symlink(fixture.repo, repoAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.worktree, checkoutAlias); err != nil {
		t.Fatal(err)
	}
	updated := updateFixtureTask(t, fixture, func(candidate *task.Task) {
		candidate.RepoPath = repoAlias
		candidate.WorktreePath = checkoutAlias
	})

	locator, err := fixture.service.LocateTask(context.Background(), updated.Task.ID)
	if err != nil {
		t.Fatalf("LocateTask: %v", err)
	}
	wantRepo, _ := pathx.Canonical(fixture.repo)
	wantCheckout, _ := pathx.Canonical(fixture.worktree)
	if locator.RepoPath != wantRepo || locator.CheckoutPath != wantCheckout || locator.TaskRevision != updated.Revision {
		t.Fatalf("canonical locator = %+v", locator)
	}
	request, err := NewRequest(locator, ParkWarmOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Plan(context.Background(), request); err != nil {
		t.Fatalf("canonical alias locator did not validate against its record: %v", err)
	}
}

func TestLifecycleLocateTaskReturnsCurrentRevision(t *testing.T) {
	fixture := newLifecycleGitFixture(t, task.ModeDirect, task.Warm)
	before, err := fixture.service.LocateTask(context.Background(), fixture.record.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated := updateFixtureTask(t, fixture, func(candidate *task.Task) {
		candidate.Next = "a revision-changing next action"
	})
	after, err := fixture.service.LocateTask(context.Background(), fixture.record.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.TaskRevision == after.TaskRevision || after.TaskRevision != updated.Revision {
		t.Fatalf("revisions before=%q after=%q current=%q", before.TaskRevision, after.TaskRevision, updated.Revision)
	}
}

func TestGenericServiceHasNoTaskLocator(t *testing.T) {
	service := NewService(nil)
	if _, err := service.LocateTask(context.Background(), "example__feature"); !errors.Is(err, ErrLocatorUnavailable) {
		t.Fatalf("generic LocateTask error = %v, want ErrLocatorUnavailable", err)
	}
}
