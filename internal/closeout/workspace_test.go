package closeout_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/closeout"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestBuildWorkspaceConvertsStructuredRepoContext(t *testing.T) {
	when := time.Date(2026, time.September, 2, 12, 30, 0, 123, time.FixedZone("offset", 2*60*60))
	tracked := &task.Task{
		ID: "repo__feat-one", Name: "Feature one", Repo: "repo", RepoPath: "/repo",
		Branch: "feat/one", Base: "main", WorktreePath: "/worktrees/one",
		State: task.Done, Owner: "host", Next: "retire", Tags: []string{"closeout"},
		AgentSession: "claude:abc", RuntimeHandle: "w1", RuntimeName: "herdr",
		Created: when.Add(-time.Hour), Updated: when,
	}
	other := &task.Task{ID: "repo__feat-cold", Repo: "repo", RepoPath: "/repo", Branch: "feat/cold", State: task.Cold}
	context := inventory.RepoContext{
		Repo: repo.Repo{
			Name: "repo", Path: "/repo", RealPath: "/physical/repo", Root: "/projects",
			Category: "Go", Symlink: true, HasGit: true,
		},
		Runtime: "herdr", WorktreeCount: 1, WorktreeErr: errors.New("worktree inventory boom"), LastActivity: when,
		Checkouts: []inventory.RepoCheckout{
			{
				Worktree:  gitx.Worktree{Path: "/repo", Branch: "main", Main: true},
				Exists:    true,
				Ownership: inventory.CheckoutCanonical,
				Status:    gitx.Status{Branch: "main", Upstream: "origin/main"},
			},
			{
				Worktree:  gitx.Worktree{Path: "/worktrees/one", Branch: "feat/one", Head: "abc123"},
				Exists:    true,
				Ownership: inventory.CheckoutDev,
				Status: gitx.Status{
					Branch: "feat/one", Upstream: "origin/feat/one", Ahead: 2, Behind: 1,
					Changed: 6, Staged: 1, Unstaged: 2, Untracked: 3,
					Added: 1, Modified: 2, Deleted: 1, Renamed: 1, Conflicted: 1,
					LatestChange: when,
				},
				LastActivity: when, LastCommit: when.Add(-time.Minute), LastSubject: "feat: one",
				Tasks: []*task.Task{tracked},
				Sessions: []runtime.Session{{
					Handle: "w1", Label: "feature", Dirs: []string{"/worktrees/one"}, AgentStatus: "idle",
					AgentSessions: []string{"claude:abc"},
					Panes:         []runtime.Pane{{ID: "w1:p1", CWD: "/worktrees/one", Agent: "claude", AgentStatus: "idle"}},
				}},
			},
			{
				Worktree:  gitx.Worktree{Path: "/worktrees/missing", Branch: "feat/missing", Prunable: true},
				Ownership: inventory.CheckoutExternal,
				StatusErr: errors.New("status boom"),
			},
			{
				Worktree:  gitx.Worktree{Path: "/repo.git", Bare: true},
				Exists:    true,
				Ownership: inventory.CheckoutExternal,
			},
		},
		OtherTasks: []*task.Task{other},
	}
	audit := retire.Audit(eligibleAuditFacts())
	report := closeout.BuildWorkspace(context, map[string]retire.AuditResult{"/worktrees/one": audit})

	if report.Repository.DisplayName != "Go/repo" || report.Repository.RealPath != "/physical/repo" {
		t.Errorf("repository = %+v", report.Repository)
	}
	if report.WorktreeInventoryAvailable {
		t.Error("failed worktree inventory reported as available")
	}
	if len(report.Checkouts) != 4 || len(report.OtherTasks) != 1 {
		t.Fatalf("checkouts=%d other_tasks=%d", len(report.Checkouts), len(report.OtherTasks))
	}
	checkout := report.Checkouts[1]
	if checkout.Path != "/worktrees/one" || checkout.Branch != "feat/one" || checkout.Ownership != "dev" || !checkout.Exists {
		t.Errorf("checkout identity = %+v", checkout)
	}
	if checkout.Status.Availability != closeout.StatusAvailable || checkout.Status.Upstream != "origin/feat/one" ||
		checkout.Status.Ahead != 2 || checkout.Status.Behind != 1 || checkout.Status.Changed != 6 ||
		checkout.Status.Staged != 1 || checkout.Status.Unstaged != 2 || checkout.Status.Untracked != 3 ||
		checkout.Status.Conflicted != 1 {
		t.Errorf("status evidence = %+v", checkout.Status)
	}
	if len(checkout.Tasks) != 1 || checkout.Tasks[0].State != "done" || checkout.Tasks[0].Mode != "worktree" {
		t.Errorf("tasks = %+v", checkout.Tasks)
	}
	if len(checkout.Sessions) != 1 || checkout.Sessions[0].WorkspaceID != "w1" || checkout.Sessions[0].Panes[0].PaneID != "w1:p1" {
		t.Errorf("sessions = %+v", checkout.Sessions)
	}
	if checkout.Retirement == nil || checkout.Retirement.Status != retire.AuditEligible {
		t.Errorf("retirement audit = %+v", checkout.Retirement)
	}
	if report.Checkouts[2].Status.Availability != closeout.StatusUnavailable {
		t.Errorf("missing checkout availability = %q", report.Checkouts[2].Status.Availability)
	}
	if report.Checkouts[3].Status.Availability != closeout.StatusNotApplicable {
		t.Errorf("bare checkout availability = %q", report.Checkouts[3].Status.Availability)
	}
	if checkout.LastActivity != "2026-09-02T10:30:00.000000123Z" {
		t.Errorf("last activity = %q", checkout.LastActivity)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	for _, forbidden := range []string{"worktree inventory boom", "status boom", "StatusErr", "WorktreeErr"} {
		if strings.Contains(jsonText, forbidden) {
			t.Errorf("serialized report contains %q: %s", forbidden, jsonText)
		}
	}

	tracked.Tags[0] = "changed"
	context.Checkouts[1].Sessions[0].Dirs[0] = "/changed"
	audit.Checks[0].Detail = "changed"
	if checkout.Tasks[0].Tags[0] != "closeout" || checkout.Sessions[0].Directories[0] != "/worktrees/one" ||
		checkout.Retirement.Checks[0].Detail == "changed" {
		t.Fatalf("report shares mutable input: %+v", checkout)
	}
}

func TestBuildWorkspaceCleanZeroStatusIsAvailable(t *testing.T) {
	report := closeout.BuildWorkspace(inventory.RepoContext{
		Checkouts: []inventory.RepoCheckout{{
			Worktree: gitx.Worktree{Path: "/repo", Branch: "main", Main: true},
			Exists:   true, Ownership: inventory.CheckoutCanonical,
		}},
	}, nil)
	if got := report.Checkouts[0].Status.Availability; got != closeout.StatusAvailable {
		t.Fatalf("zero clean status availability = %q", got)
	}
	if report.Checkouts[0].Tasks == nil || report.Checkouts[0].Sessions == nil || report.OtherTasks == nil {
		t.Fatalf("empty collections should be JSON arrays: %+v", report)
	}
}

func eligibleAuditFacts() retire.AuditInput {
	return retire.AuditInput{
		TargetKind: retire.AuditTargetDev, Registered: retire.KnownFact(true), Unlocked: retire.KnownFact(true),
		BranchNamed: retire.KnownFact(true), IdentityMatches: retire.KnownFact(true),
		PathExists: retire.KnownFact(true), Dirty: retire.KnownFact(false), InProgress: retire.KnownFact(false),
		BaseKnown: true, Contained: retire.KnownFact(true), TaskPresent: retire.KnownFact(false),
		ArtifactKnown: true, Finalized: true, RuntimeKnown: true, RuntimeReady: true,
	}
}
