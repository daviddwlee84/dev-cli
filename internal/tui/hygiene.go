package tui

import tea "github.com/charmbracelet/bubbletea"

// HygieneRequest always identifies a checkout, never the process cwd or just
// the common repository. The CLI owns scanning, rendering and setup policy.
type HygieneRequest struct {
	Operation string
	Checkout  string
	Scope     string
}

func hygieneTargetApplicable(c ActionContext) bool {
	return c.repo != nil && c.repo.Repo.Repo.HasGit && !c.repo.Repo.Repo.Bare
}

func hygieneReady(c ActionContext) string {
	if c.repo != nil && c.repo.child() {
		checkout, ok := c.repo.checkout()
		if !ok || !checkout.Exists || checkout.Worktree.Prunable {
			return "Worktree checkout is missing or prunable"
		}
	}
	return requires(c.model.actions.Workflow != nil, "Repository hygiene workflow")
}

func (m Model) hygieneCheckout() (string, bool) {
	item, ok := m.currentRepoItem()
	if !ok {
		return "", false
	}
	if item.child() {
		checkout, ok := item.checkout()
		if !ok || !checkout.Exists || checkout.Worktree.Prunable {
			return "", false
		}
		return checkout.Worktree.Path, true
	}
	return item.Repo.Repo.Path, item.Repo.Repo.Path != ""
}

func (m Model) runHygieneAction(action listAction) (tea.Model, tea.Cmd) {
	checkout, ok := m.hygieneCheckout()
	if !ok {
		return m, nil
	}
	if action == listActionHygieneMenu {
		menu := overlayState{kind: overlayActionMenu, title: "Repository hygiene", subject: "Checkout: " + contract(checkout), selection: m.currentToken()}
		m.addRegisteredActions(&menu, menuHygiene)
		m.overlay = menu
		return m, nil
	}
	if action == listActionHygieneScan {
		menu := overlayState{kind: overlayActionMenu, title: "Hygiene scan scope", subject: "Checkout: " + contract(checkout), selection: m.currentToken(), body: "Scan the working files, staged index, or local Git history. Scanning never fetches or modifies source files."}
		for _, id := range []listAction{listActionHygieneScanWorktree, listActionHygieneScanStaged, listActionHygieneScanHistory} {
			spec, _ := findAction(m.view, id)
			menu.addOption(id, spec.Label)
		}
		m.overlay = menu
		return m, nil
	}
	request := HygieneRequest{Checkout: checkout}
	switch action {
	case listActionHygieneStatus:
		request.Operation = "status"
	case listActionHygieneReport:
		request.Operation = "report"
	case listActionHygieneScanWorktree:
		request.Operation, request.Scope = "scan", "worktree"
	case listActionHygieneScanStaged:
		request.Operation, request.Scope = "scan", "staged"
	case listActionHygieneScanHistory:
		request.Operation, request.Scope = "scan", "history"
	default:
		return m, nil
	}
	return m.runWorkflow(WorkflowRequest{Action: "hygiene", Hygiene: &request, LocalGeneration: m.localGeneration})
}
