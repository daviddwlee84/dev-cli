package closeout

import (
	"time"

	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// StatusAvailability distinguishes a successfully observed clean zero-value
// status from a status that could not or should not be collected.
type StatusAvailability string

const (
	StatusAvailable     StatusAvailability = "available"
	StatusUnavailable   StatusAvailability = "unavailable"
	StatusNotApplicable StatusAvailability = "not-applicable"
)

// RepositoryEvidence identifies the repository represented by a workspace
// report. Paths are intentionally machine-local evidence.
type RepositoryEvidence struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Path        string `json:"path"`
	RealPath    string `json:"real_path"`
	Root        string `json:"root"`
	Category    string `json:"category"`
	Symlink     bool   `json:"symlink"`
	Bare        bool   `json:"bare"`
	HasGit      bool   `json:"has_git"`
}

// GitStatusEvidence is a JSON-safe copy of a checkout's live status.
type GitStatusEvidence struct {
	Availability StatusAvailability `json:"availability"`
	Branch       string             `json:"branch"`
	Detached     bool               `json:"detached"`
	Upstream     string             `json:"upstream"`
	Ahead        int                `json:"ahead"`
	Behind       int                `json:"behind"`
	Changed      int                `json:"changed"`
	Staged       int                `json:"staged"`
	Unstaged     int                `json:"unstaged"`
	Untracked    int                `json:"untracked"`
	Added        int                `json:"added"`
	Modified     int                `json:"modified"`
	Deleted      int                `json:"deleted"`
	Renamed      int                `json:"renamed"`
	Conflicted   int                `json:"conflicted"`
	LatestChange string             `json:"latest_change,omitempty"`
}

// TaskEvidence is the durable task intent relevant to closeout decisions.
type TaskEvidence struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Repo          string   `json:"repo"`
	RepoPath      string   `json:"repo_path"`
	Branch        string   `json:"branch"`
	Base          string   `json:"base"`
	WorktreePath  string   `json:"worktree_path"`
	Mode          string   `json:"mode"`
	State         string   `json:"state"`
	Owner         string   `json:"owner"`
	Next          string   `json:"next"`
	Note          string   `json:"note"`
	Tags          []string `json:"tags"`
	AgentSession  string   `json:"agent_session"`
	RuntimeHandle string   `json:"runtime_handle"`
	RuntimeName   string   `json:"runtime_name"`
	Created       string   `json:"created,omitempty"`
	Updated       string   `json:"updated,omitempty"`
}

// CheckoutEvidence is one canonical or linked checkout in repository context.
type CheckoutEvidence struct {
	Path           string              `json:"path"`
	Branch         string              `json:"branch"`
	Ownership      string              `json:"ownership"`
	Exists         bool                `json:"exists"`
	Main           bool                `json:"main"`
	Head           string              `json:"head"`
	Detached       bool                `json:"detached"`
	Bare           bool                `json:"bare"`
	Locked         bool                `json:"locked"`
	LockedReason   string              `json:"locked_reason"`
	Prunable       bool                `json:"prunable"`
	PrunableReason string              `json:"prunable_reason"`
	Status         GitStatusEvidence   `json:"status"`
	LastActivity   string              `json:"last_activity,omitempty"`
	LastCommit     string              `json:"last_commit,omitempty"`
	LastSubject    string              `json:"last_subject"`
	Tasks          []TaskEvidence      `json:"tasks"`
	Sessions       []SessionEvidence   `json:"sessions"`
	Retirement     *retire.AuditResult `json:"retirement,omitempty"`
}

// WorkspaceReport is the structured, read-only form of inventory.RepoContext.
// Forge pull-request evidence is deliberately outside this domain conversion.
type WorkspaceReport struct {
	Repository                 RepositoryEvidence `json:"repository"`
	Runtime                    string             `json:"runtime"`
	WorktreeCount              int                `json:"worktree_count"`
	WorktreeInventoryAvailable bool               `json:"worktree_inventory_available"`
	LastActivity               string             `json:"last_activity,omitempty"`
	Checkouts                  []CheckoutEvidence `json:"checkouts"`
	OtherTasks                 []TaskEvidence     `json:"other_tasks"`
}

// BuildWorkspace converts an already-collected repository context. audits is
// optional and keyed by checkout path; the result does not share mutable slices
// with either input.
func BuildWorkspace(context inventory.RepoContext, audits map[string]retire.AuditResult) WorkspaceReport {
	report := WorkspaceReport{
		Repository: RepositoryEvidence{
			Name: context.Repo.Name, DisplayName: context.Repo.Display(),
			Path: context.Repo.Path, RealPath: context.Repo.RealPath,
			Root: context.Repo.Root, Category: context.Repo.Category,
			Symlink: context.Repo.Symlink, Bare: context.Repo.Bare, HasGit: context.Repo.HasGit,
		},
		Runtime:                    context.Runtime,
		WorktreeCount:              context.WorktreeCount,
		WorktreeInventoryAvailable: context.WorktreeErr == nil,
		LastActivity:               timestamp(context.LastActivity),
		Checkouts:                  make([]CheckoutEvidence, 0, len(context.Checkouts)),
		OtherTasks:                 tasksEvidence(context.OtherTasks),
	}
	for _, checkout := range context.Checkouts {
		report.Checkouts = append(report.Checkouts, checkoutEvidence(checkout, audits))
	}
	return report
}

func checkoutEvidence(checkout inventory.RepoCheckout, audits map[string]retire.AuditResult) CheckoutEvidence {
	out := CheckoutEvidence{
		Path: checkout.Worktree.Path, Branch: checkout.Branch(),
		Ownership: string(checkout.Ownership), Exists: checkout.Exists,
		Main: checkout.Worktree.Main, Head: checkout.Worktree.Head,
		Detached: checkout.Worktree.Detached || checkout.Status.Detached,
		Bare:     checkout.Worktree.Bare, Locked: checkout.Worktree.Locked,
		LockedReason: checkout.Worktree.LockedReason,
		Prunable:     checkout.Worktree.Prunable, PrunableReason: checkout.Worktree.PrunableReason,
		Status:       statusEvidence(checkout),
		LastActivity: timestamp(checkout.LastActivity), LastCommit: timestamp(checkout.LastCommit),
		LastSubject: checkout.LastSubject,
		Tasks:       tasksEvidence(checkout.Tasks),
		Sessions:    sessionsEvidence(checkout.Sessions),
	}
	if audit, ok := audits[checkout.Worktree.Path]; ok {
		copy := audit
		copy.Checks = append([]retire.AuditCheck(nil), audit.Checks...)
		out.Retirement = &copy
	}
	return out
}

func statusEvidence(checkout inventory.RepoCheckout) GitStatusEvidence {
	availability := StatusAvailable
	switch {
	case checkout.Worktree.Bare:
		availability = StatusNotApplicable
	case !checkout.Exists || checkout.Worktree.Prunable || checkout.StatusErr != nil:
		availability = StatusUnavailable
	}
	status := checkout.Status
	return GitStatusEvidence{
		Availability: availability,
		Branch:       status.Branch, Detached: status.Detached,
		Upstream: status.Upstream, Ahead: status.Ahead, Behind: status.Behind,
		Changed: status.Changed, Staged: status.Staged, Unstaged: status.Unstaged,
		Untracked: status.Untracked, Added: status.Added, Modified: status.Modified,
		Deleted: status.Deleted, Renamed: status.Renamed, Conflicted: status.Conflicted,
		LatestChange: timestamp(status.LatestChange),
	}
}

func tasksEvidence(tasks []*task.Task) []TaskEvidence {
	out := make([]TaskEvidence, 0, len(tasks))
	for _, item := range tasks {
		if item == nil {
			continue
		}
		out = append(out, TaskEvidence{
			ID: item.ID, Name: item.Name, Repo: item.Repo, RepoPath: item.RepoPath,
			Branch: item.Branch, Base: item.Base, WorktreePath: item.WorktreePath,
			Mode: string(item.EffectiveMode()), State: string(item.State), Owner: item.Owner,
			Next: item.Next, Note: item.Note, Tags: cloneStrings(item.Tags),
			AgentSession: item.AgentSession, RuntimeHandle: item.RuntimeHandle,
			RuntimeName: item.RuntimeName,
			Created:     timestamp(item.Created), Updated: timestamp(item.Updated),
		})
	}
	return out
}

func sessionsEvidence(sessions []runtime.Session) []SessionEvidence {
	out := make([]SessionEvidence, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, sessionEvidence(session))
	}
	return out
}

func timestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
