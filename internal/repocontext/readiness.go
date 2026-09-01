package repocontext

import (
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/assessment"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// Readiness is one cheap, local-only context projection. It intentionally
// carries no authority to mutate; the full Report binds the same result to
// point-in-time evidence sources.
type Readiness struct {
	Outcome assessment.Outcome
	Reasons []assessment.Reason
}

// LocalReadiness keeps independently actionable checkout, task, and worktree
// scopes separate. There is deliberately no global safe boolean.
type LocalReadiness struct {
	Checkout Readiness
	Task     Readiness
	Worktree Readiness
}

// Summary is the compact status/TUI projection.
func (r LocalReadiness) Summary() string {
	return strings.Join([]string{
		"checkout " + string(r.Checkout.Outcome),
		"task " + string(r.Task.Outcome),
		"worktree " + string(r.Worktree.Outcome),
	}, " · ")
}

// AssessLocal derives readiness from facts already collected by inventory. It
// does not run Git, inspect a runtime, or contact a forge/fleet host.
func AssessLocal(context inventory.RepoContext, selectedIndex int, hostname string) LocalReadiness {
	return LocalReadiness{
		Checkout: assessCheckout(context, selectedIndex),
		Task:     assessTasks(context, selectedIndex, hostname),
		Worktree: assessWorktrees(context, selectedIndex),
	}
}

func assessCheckout(context inventory.RepoContext, selectedIndex int) Readiness {
	if selectedIndex < 0 || selectedIndex >= len(context.Checkouts) {
		return indeterminate("checkout-selection-unavailable", "selected-checkout", "the selected checkout is absent from the local inventory", "rerun context from an existing checkout")
	}
	checkout := context.Checkouts[selectedIndex]
	switch {
	case checkout.Worktree.Bare || context.Repo.Bare:
		return Readiness{Outcome: assessment.OutcomeNotApplicable}
	case checkout.PathErr != nil:
		return indeterminate("checkout-path-unavailable", "selected-checkout", "the checkout path could not be inspected", "restore path access and rerun context")
	case !checkout.Exists || checkout.Worktree.Prunable:
		return blocked("checkout-missing", "selected-checkout", "the checkout path is missing or prunable", "repair or remove the stale worktree registration")
	case checkout.StatusErr != nil:
		return indeterminate("checkout-status-unavailable", "selected-checkout", "Git status could not be collected", "repair the checkout and rerun context")
	case checkout.Worktree.Detached || checkout.Status.Detached || checkout.Branch() == "":
		return blocked("checkout-detached", "selected-checkout", "the checkout does not have an attached branch", "check out an explicit branch")
	case checkout.Status.Conflicted > 0:
		return blocked("checkout-conflicted", "selected-checkout", fmt.Sprintf("the checkout has %d unmerged path(s)", checkout.Status.Conflicted), "resolve the Git operation before transfer or retirement")
	case checkout.Status.Dirty():
		return blocked("checkout-dirty", "selected-checkout", fmt.Sprintf("the checkout has %d changed path(s)", checkout.Status.Changed), "commit or deliberately preserve the changes")
	default:
		return Readiness{Outcome: assessment.OutcomeEligible}
	}
}

func assessTasks(context inventory.RepoContext, selectedIndex int, hostname string) Readiness {
	if context.TaskErr != nil {
		return indeterminate("task-inventory-unavailable", "task-inventory", "task records could not be collected", "repair the task store and rerun context")
	}
	if selectedIndex < 0 || selectedIndex >= len(context.Checkouts) {
		return indeterminate("checkout-selection-unavailable", "selected-checkout", "task scope cannot be selected without a checkout", "rerun context from an existing checkout")
	}
	// Transfer/repository readiness covers every non-DONE task attached to the
	// clone, including cold and branch-only records without a local checkout.
	// Looking only at the selected checkout can otherwise hide another machine's
	// ownership claim and incorrectly report not-applicable.
	active := make([]*task.Task, 0, len(context.OtherTasks)+len(context.Checkouts[selectedIndex].Tasks))
	sessionCount := map[string]int{}
	seen := map[string]struct{}{}
	add := func(candidate *task.Task, sessions int) {
		if candidate == nil || candidate.State == task.Done {
			return
		}
		if _, exists := seen[candidate.ID]; exists {
			return
		}
		seen[candidate.ID] = struct{}{}
		active = append(active, candidate)
		sessionCount[candidate.ID] = sessions
	}
	for _, checkout := range context.Checkouts {
		for _, candidate := range checkout.Tasks {
			add(candidate, len(checkout.Sessions))
		}
	}
	for _, candidate := range context.OtherTasks {
		add(candidate, 0)
	}
	if len(active) == 0 {
		return Readiness{Outcome: assessment.OutcomeNotApplicable}
	}
	var reasons []assessment.Reason
	hasIndeterminate := false
	for _, candidate := range active {
		if err := candidate.Validate(); err != nil {
			hasIndeterminate = true
			reasons = append(reasons, assessment.Reason{
				Code: "task-record-invalid", Subject: "task", Detail: "a selected task record is invalid",
				Remediation: "repair or recreate the task record",
			})
			continue
		}
		if hostname != "" && candidate.Owner != "" && candidate.Owner != hostname {
			reasons = append(reasons, assessment.Reason{
				Code: "task-owned-elsewhere", Subject: "task", Detail: "a selected task is owned by another machine",
				Remediation: "complete an explicit ownership handoff before writing",
			})
		}
		if candidate.State == task.Hot {
			if context.RuntimeErr != nil {
				hasIndeterminate = true
				reasons = append(reasons, assessment.Reason{
					Code: "runtime-inventory-unavailable", Subject: "task", Detail: "a HOT task cannot be checked against live runtime state",
					Remediation: "restore runtime inspection and rerun context",
				})
			} else if sessionCount[candidate.ID] == 0 {
				reasons = append(reasons, assessment.Reason{
					Code: "hot-task-runtime-missing", Subject: "task", Detail: "a HOT task has no observed runtime session",
					Remediation: "resume the task or reconcile its lifecycle state",
				})
			}
		}
	}
	if hasReason(reasons, "task-owned-elsewhere") || hasReason(reasons, "hot-task-runtime-missing") {
		return Readiness{Outcome: assessment.OutcomeBlocked, Reasons: reasons}
	}
	if hasIndeterminate {
		return Readiness{Outcome: assessment.OutcomeIndeterminate, Reasons: reasons}
	}
	return Readiness{Outcome: assessment.OutcomeEligible}
}

func assessWorktrees(context inventory.RepoContext, selectedIndex int) Readiness {
	if context.WorktreeErr != nil {
		return indeterminate("worktree-inventory-unavailable", "worktrees", "linked worktrees could not be enumerated", "repair Git worktree metadata and rerun context")
	}
	if selectedIndex < 0 || selectedIndex >= len(context.Checkouts) {
		return indeterminate("checkout-selection-unavailable", "selected-checkout", "worktree scope cannot be selected without a checkout", "rerun context from an existing checkout")
	}
	indices := []int{}
	if selectedIndex > 0 {
		indices = append(indices, selectedIndex)
	} else {
		for index := 1; index < len(context.Checkouts); index++ {
			indices = append(indices, index)
		}
	}
	if len(indices) == 0 {
		return Readiness{Outcome: assessment.OutcomeNotApplicable}
	}
	var reasons []assessment.Reason
	hasUnknown := context.RuntimeErr != nil
	if hasUnknown {
		reasons = append(reasons, assessment.Reason{
			Code: "runtime-inventory-unavailable", Subject: "worktrees",
			Detail:      "worktree runtime coverage could not be collected",
			Remediation: "restore runtime inspection and rerun context",
		})
	}
	for _, index := range indices {
		checkout := context.Checkouts[index]
		subject := fmt.Sprintf("worktree:%d", index)
		switch {
		case checkout.PathErr != nil:
			hasUnknown = true
			reasons = append(reasons, assessment.Reason{Code: "worktree-path-unavailable", Subject: subject, Detail: "the worktree path could not be inspected", Remediation: "restore path access and rerun context"})
		case !checkout.Exists || checkout.Worktree.Prunable:
			reasons = append(reasons, assessment.Reason{Code: "worktree-missing", Subject: subject, Detail: "the worktree path is missing or prunable", Remediation: "repair or remove the stale worktree registration"})
		case checkout.StatusErr != nil:
			hasUnknown = true
			reasons = append(reasons, assessment.Reason{Code: "worktree-status-unavailable", Subject: subject, Detail: "Git status could not be collected for the worktree", Remediation: "repair the worktree and rerun context"})
		case checkout.Worktree.Locked:
			reasons = append(reasons, assessment.Reason{Code: "worktree-locked", Subject: subject, Detail: "the worktree is locked", Remediation: "verify the lock owner before unlocking it"})
		case checkout.Status.Conflicted > 0:
			reasons = append(reasons, assessment.Reason{Code: "worktree-conflicted", Subject: subject, Detail: "the worktree has unmerged paths", Remediation: "resolve the Git operation before transfer or retirement"})
		case checkout.Status.Dirty():
			reasons = append(reasons, assessment.Reason{Code: "worktree-dirty", Subject: subject, Detail: "the worktree contains uncommitted changes", Remediation: "commit or deliberately preserve the changes"})
		case len(checkout.Sessions) > 0:
			reasons = append(reasons, assessment.Reason{Code: "worktree-runtime-live", Subject: subject, Detail: "a runtime session still covers the worktree", Remediation: "quiesce and close the runtime before retirement"})
		}
	}
	if len(reasons) == 0 {
		return Readiness{Outcome: assessment.OutcomeEligible}
	}
	if hasKnownBlocker(reasons) {
		return Readiness{Outcome: assessment.OutcomeBlocked, Reasons: reasons}
	}
	if hasUnknown {
		return Readiness{Outcome: assessment.OutcomeIndeterminate, Reasons: reasons}
	}
	return Readiness{Outcome: assessment.OutcomeBlocked, Reasons: reasons}
}

func blocked(code, subject, detail, remediation string) Readiness {
	return Readiness{Outcome: assessment.OutcomeBlocked, Reasons: []assessment.Reason{{Code: code, Subject: subject, Detail: detail, Remediation: remediation}}}
}

func indeterminate(code, subject, detail, remediation string) Readiness {
	return Readiness{Outcome: assessment.OutcomeIndeterminate, Reasons: []assessment.Reason{{Code: code, Subject: subject, Detail: detail, Remediation: remediation}}}
}

func hasReason(reasons []assessment.Reason, code string) bool {
	for _, reason := range reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func hasKnownBlocker(reasons []assessment.Reason) bool {
	for _, reason := range reasons {
		switch reason.Code {
		case "worktree-missing", "worktree-locked", "worktree-conflicted", "worktree-dirty", "worktree-runtime-live":
			return true
		}
	}
	return false
}
