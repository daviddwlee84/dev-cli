// Package closeout builds read-only evidence for runtime and workspace
// closeout decisions. It does not close sessions or mutate repository state.
package closeout

import (
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

// RuntimeCloseStatus classifies runtime closure evidence only. In particular,
// close-eligible does not mean that task intent or repository work is complete.
type RuntimeCloseStatus string

const (
	RuntimeCloseBlocked  RuntimeCloseStatus = "blocked"
	RuntimeCloseEligible RuntimeCloseStatus = "close-eligible"
	RuntimeCloseUnknown  RuntimeCloseStatus = "unknown"

	RuntimeCloseScope   = "runtime-closure-only"
	RuntimeCloseMeaning = "runtime closure only; not task or work completion"
)

// TargetMapping is caller-collected evidence mapping one runtime session to a
// target checkout. The builder deliberately does not rediscover coverage.
type TargetMapping struct {
	CheckoutPath    string   `json:"checkout_path,omitempty"`
	CoverageKnown   bool     `json:"coverage_known"`
	CoversTarget    bool     `json:"covers_target"`
	CoveringPaneIDs []string `json:"covering_pane_ids"`
	MixedPaneIDs    []string `json:"mixed_pane_ids"`
	CallerContained bool     `json:"caller_contained"`
	MixedPurpose    bool     `json:"mixed_purpose"`
}

// PaneEvidence is the JSON-safe runtime observation for one pane.
type PaneEvidence struct {
	PaneID       string `json:"pane_id,omitempty"`
	CWD          string `json:"cwd,omitempty"`
	ShellCWD     string `json:"shell_cwd,omitempty"`
	Agent        string `json:"agent,omitempty"`
	AgentStatus  string `json:"agent_status,omitempty"`
	AgentSession string `json:"agent_session,omitempty"`
}

// AgentActivityEvidence is the JSON-safe observation for one recognized agent.
type AgentActivityEvidence struct {
	PaneID      string `json:"pane_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	CWD         string `json:"cwd,omitempty"`
}

// SessionEvidence preserves the runtime identifiers and paths behind a close
// classification without exposing a runtime implementation.
type SessionEvidence struct {
	WorkspaceID   string         `json:"workspace_id,omitempty"`
	Label         string         `json:"label,omitempty"`
	Directories   []string       `json:"directories"`
	AgentStatus   string         `json:"agent_status,omitempty"`
	AgentSessions []string       `json:"agent_sessions"`
	Panes         []PaneEvidence `json:"panes"`
	Focused       bool           `json:"focused"`
}

// SessionCloseReport is a read-only runtime-closure classification and its
// supporting evidence. No value in this report authorizes task or Git cleanup.
type SessionCloseReport struct {
	RuntimeCloseStatus  RuntimeCloseStatus      `json:"runtime_close_status"`
	RuntimeCloseScope   string                  `json:"runtime_close_scope"`
	RuntimeCloseMeaning string                  `json:"runtime_close_meaning"`
	Details             []string                `json:"details"`
	Target              TargetMapping           `json:"target"`
	Session             SessionEvidence         `json:"session"`
	ActivitiesAvailable bool                    `json:"activities_available"`
	Activities          []AgentActivityEvidence `json:"activities"`
}

// BuildSession classifies one already-collected runtime session. activities may
// be a machine-wide snapshot; nil means agent activity coverage was unavailable,
// while a non-nil empty slice is a successful observation with no activities.
func BuildSession(session runtime.Session, activities []runtime.AgentActivity, target TargetMapping) SessionCloseReport {
	target.CoveringPaneIDs = cloneStrings(target.CoveringPaneIDs)
	target.MixedPaneIDs = cloneStrings(target.MixedPaneIDs)
	target.CoversTarget = target.CoversTarget || len(target.CoveringPaneIDs) > 0
	target.MixedPurpose = target.MixedPurpose || len(target.MixedPaneIDs) > 0

	report := SessionCloseReport{
		RuntimeCloseScope:   RuntimeCloseScope,
		RuntimeCloseMeaning: RuntimeCloseMeaning,
		Target:              target,
		Session:             sessionEvidence(session),
		ActivitiesAvailable: activities != nil,
		Activities:          make([]AgentActivityEvidence, 0),
		Details:             make([]string, 0),
	}

	paneIDs := make(map[string]bool, len(session.Panes)+len(target.CoveringPaneIDs)+len(target.MixedPaneIDs))
	for _, pane := range session.Panes {
		paneIDs[pane.ID] = true
	}
	for _, paneID := range target.CoveringPaneIDs {
		paneIDs[paneID] = true
	}
	for _, paneID := range target.MixedPaneIDs {
		paneIDs[paneID] = true
	}
	statusSet := make(map[string]bool)
	activityPanes := make(map[string]bool)
	for _, activity := range activities {
		if !activityBelongsToSession(activity, session.Handle, paneIDs) {
			continue
		}
		report.Activities = append(report.Activities, agentActivityEvidence(activity))
		if relevantPane(activity.PaneID, target.CoveringPaneIDs) {
			activityPanes[activity.PaneID] = true
			statusSet[normalizedStatus(activity.Status)] = true
		}
	}
	for _, pane := range session.Panes {
		if !relevantPane(pane.ID, target.CoveringPaneIDs) {
			continue
		}
		if pane.AgentStatus != "" {
			statusSet[normalizedStatus(pane.AgentStatus)] = true
		} else if (pane.Agent != "" || pane.AgentSession != "") && !activityPanes[pane.ID] {
			statusSet[""] = true
		}
	}
	if len(statusSet) == 0 && session.AgentStatus != "" {
		statusSet[normalizedStatus(session.AgentStatus)] = true
	}

	blocked := false
	unknown := false
	if target.CallerContained {
		blocked = true
		report.Details = append(report.Details, "caller is contained in the runtime session")
	}
	if target.MixedPurpose {
		blocked = true
		report.Details = append(report.Details, "runtime session also contains panes outside the target checkout")
	}
	if !target.CoverageKnown {
		unknown = true
		report.Details = append(report.Details, "target checkout coverage is unknown")
	} else if !target.CoversTarget {
		unknown = true
		report.Details = append(report.Details, "runtime session is not mapped to a target checkout")
	}

	statuses := make([]string, 0, len(statusSet))
	for status := range statusSet {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	if len(statuses) == 0 {
		unknown = true
		report.Details = append(report.Details, "agent activity and status evidence is unavailable")
	}
	for _, status := range statuses {
		switch retire.ClassifyAgentStatus(status) {
		case retire.AgentCloseEligible:
			// These states pass only the runtime activity gate.
		case retire.AgentCloseBlocked:
			blocked = true
			report.Details = append(report.Details, "agent status "+status+" blocks runtime closure")
		case retire.AgentCloseUnknown:
			unknown = true
			if status == "" || status == "unknown" {
				report.Details = append(report.Details, "agent status is unknown")
			} else {
				report.Details = append(report.Details, "agent status "+status+" is unrecognized")
			}
		}
	}

	switch {
	case blocked:
		report.RuntimeCloseStatus = RuntimeCloseBlocked
	case unknown:
		report.RuntimeCloseStatus = RuntimeCloseUnknown
	default:
		report.RuntimeCloseStatus = RuntimeCloseEligible
		report.Details = append(report.Details, "runtime closure is structurally eligible and all recognized covering agents are idle or done")
	}
	return report
}

func sessionEvidence(session runtime.Session) SessionEvidence {
	out := SessionEvidence{
		WorkspaceID:   session.Handle,
		Label:         session.Label,
		Directories:   cloneStrings(session.Dirs),
		AgentStatus:   session.AgentStatus,
		AgentSessions: cloneStrings(session.AgentSessions),
		Panes:         make([]PaneEvidence, 0, len(session.Panes)),
		Focused:       session.Focused,
	}
	for _, pane := range session.Panes {
		out.Panes = append(out.Panes, PaneEvidence{
			PaneID: pane.ID, CWD: pane.CWD, ShellCWD: pane.ShellCWD,
			Agent: pane.Agent, AgentStatus: pane.AgentStatus, AgentSession: pane.AgentSession,
		})
	}
	return out
}

func agentActivityEvidence(activity runtime.AgentActivity) AgentActivityEvidence {
	return AgentActivityEvidence{
		PaneID: activity.PaneID, WorkspaceID: activity.WorkspaceID,
		Agent: activity.Agent, Name: activity.Name, Status: activity.Status, CWD: activity.CWD,
	}
}

func activityBelongsToSession(activity runtime.AgentActivity, workspaceID string, paneIDs map[string]bool) bool {
	if activity.WorkspaceID != "" {
		return activity.WorkspaceID == workspaceID
	}
	return paneIDs[activity.PaneID]
}

func relevantPane(paneID string, covering []string) bool {
	if len(covering) == 0 {
		return true
	}
	for _, id := range covering {
		if paneID == id {
			return true
		}
	}
	return false
}

func normalizedStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}
