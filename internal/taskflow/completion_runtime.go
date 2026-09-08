package taskflow

import (
	"context"
	"fmt"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func retirementPreviewCondition(expected string, inspection retire.Inspection) Condition {
	if expected != "" && expected != inspection.Fingerprint() {
		return condition("runtime-preview-current", VerdictBlocked, RequirementRequired, "retirement preview is stale: runtime topology or foreground programs changed", "review the current runtime before authorizing cleanup")
	}
	return condition("runtime-preview-current", VerdictMet, RequirementRequired, "retirement runtime matches its approved preview", "")
}

// RetirementPreviewAuthority keeps task/Git/artifact ownership bound across
// interactive permissions and caller handoff, independently of runtime consent.
func RetirementPreviewAuthority(plan Plan) Fields {
	fields := make(map[string]string)
	for key, value := range plan.AuthorityFields() {
		if strings.HasPrefix(key, "task.") || strings.HasPrefix(key, "destructive.") && !strings.HasPrefix(key, "destructive.runtime") {
			fields[key] = value
		}
	}
	return NewFields(fields)
}

func retirementIdentityPreviewCondition(expected Fields, actual map[string]string) Condition {
	for _, field := range expected.Entries() {
		if actual[field.Key] != field.Value {
			return condition("retirement-preview-current", VerdictBlocked, RequirementRequired, "retirement preview is stale: "+field.Key+" changed", "refresh the exact task, Git and artifact preview before cleanup")
		}
	}
	return condition("retirement-preview-current", VerdictMet, RequirementRequired, "task, Git and artifact ownership match the retirement preview", "")
}

// CompletionRuntimeOptions contains exact, run-local user choices. No CLI
// blanket flag manufactures these acknowledgements.
type CompletionRuntimeOptions struct {
	CloseTaskPanes Fields
	ProgramConsent Fields
}

func (r CompletionRuntimeOptions) clone() CompletionRuntimeOptions {
	return CompletionRuntimeOptions{CloseTaskPanes: r.CloseTaskPanes.clone(), ProgramConsent: r.ProgramConsent.clone()}
}

func completionRuntimeOptions(options ActionOptions) CompletionRuntimeOptions {
	switch v := options.(type) {
	case CompleteDirectOptions:
		return v.Runtime
	case CompleteFFOptions:
		return v.Runtime
	case ReviewHandoffOptions:
		return v.Runtime
	case VerifyMergedOptions:
		return v.Runtime
	}
	return CompletionRuntimeOptions{}
}

// CloseableTaskPanes is intentionally limited to linked task checkouts. It
// never offers canonical agents, callers, mixed or parent workspaces.
func CloseableTaskPanes(mode task.CheckoutMode, occupancy runtime.Occupancy) map[string]string {
	eligible := make(map[string]string)
	if mode != task.ModeWorktree || occupancy.Backend != "herdr" || occupancyObservationError(occupancy, nil) != nil {
		return eligible
	}
	for _, agent := range occupancy.Agents {
		if agent.IsCaller || (agent.Status != "idle" && agent.Status != "done") {
			continue
		}
		for _, session := range occupancy.Sessions {
			if session.Runtime.Handle != agent.SessionHandle || len(session.Mixed) > 0 || runtime.WorkspaceProtection(session.Runtime, occupancy.Target) != "" {
				continue
			}
			for _, pane := range session.Panes {
				if pane.ID != agent.Activity.PaneID || pane.Agent != agent.Activity.Agent || pane.AgentStatus != agent.Status {
					continue
				}
				if pane.Process != nil && (pane.Process.State == runtime.ProcessUnknown || pane.Process.Error != "") {
					continue
				}
				eligible[pane.ID] = runtime.PaneFingerprint(pane)
			}
		}
	}
	return eligible
}

func completionPaneError(o lifecycleObservation) error {
	approved := o.completionRuntime.CloseTaskPanes.Entries()
	if len(approved) == 0 {
		return nil
	}
	if o.completionClaimsErr != nil {
		return o.completionClaimsErr
	}
	if o.mode != task.ModeWorktree || !o.isLinkedWorktree() {
		return fmt.Errorf("canonical agent panes must be closed independently through Herdr")
	}
	if _, ok := o.runtime.(runtime.PaneCloser); !ok {
		return fmt.Errorf("runtime cannot close an exact task pane")
	}
	eligible := CloseableTaskPanes(o.mode, o.occupancy)
	for _, entry := range approved {
		if eligible[entry.Key] != entry.Value || entry.Value == "" {
			return staleBoundary("task agent pane " + entry.Key + " is no longer eligible for the approved close")
		}
	}
	return nil
}

func completionPaneCondition(o lifecycleObservation) Condition {
	if err := completionPaneError(o); err != nil {
		return condition(ConditionTaskPaneClosure, VerdictBlocked, RequirementRequired, err.Error(), "refresh the task pane preview; parent panes must be handled independently")
	}
	return condition(ConditionTaskPaneClosure, VerdictMet, RequirementRequired, "only selected exact idle/done task panes may close after final approval", "")
}

func completionAllowedOccupancy(o lifecycleObservation) runtime.Occupancy {
	occupancy := o.occupancy
	if completionPaneError(o) != nil {
		return occupancy
	}
	approved := o.completionRuntime.CloseTaskPanes.Map()
	occupancy.Agents = append([]runtime.OccupancyAgent(nil), occupancy.Agents...)
	for i, agent := range occupancy.Agents {
		if approved[agent.Activity.PaneID] != "" {
			occupancy.Agents[i].Blocking = false
		}
	}
	return occupancy
}

func completionPaneEffects(o lifecycleObservation) []Effect {
	var effects []Effect
	for _, entry := range o.completionRuntime.CloseTaskPanes.Entries() {
		effects = append(effects, NewEffect(EffectCloseTaskPane, "close the selected idle/done task agent pane", entry.Key, true, false,
			map[string]string{"pane": entry.Key, "fingerprint": entry.Value, "checkout": o.checkout}))
	}
	return effects
}

func completionProgramError(o lifecycleObservation) error {
	for _, scope := range []struct {
		enabled   bool
		occupancy runtime.Occupancy
	}{
		{o.checkTaskPrograms, o.occupancy}, {o.checkParentPrograms, o.integration.occupancy},
	} {
		if !scope.enabled {
			continue
		}
		fingerprint, err := runtime.ForegroundFingerprint(scope.occupancy)
		if err != nil {
			return err
		}
		consent := o.completionRuntime.ProgramConsent.Map()[scope.occupancy.Target]
		if fingerprint != consent {
			return fmt.Errorf("foreground programs in %s require a fresh explicit confirmation that checkout files will change while those programs keep running", scope.occupancy.Target)
		}
	}
	return nil
}

func completionProgramCondition(o lifecycleObservation) Condition {
	if err := completionProgramError(o); err != nil {
		return condition(ConditionForegroundPrograms, VerdictBlocked, RequirementRequired, err.Error(), "review the foreground programs interactively, or stop them and retry")
	}
	return condition(ConditionForegroundPrograms, VerdictMet, RequirementRequired, "foreground program consent matches the planned checkout writes", "")
}

func (e *executionState) applyCompletionPaneClosure(ctx context.Context, effect Effect) error {
	o := &e.observed
	o.completionClaimsErr = e.service.completionTaskClaimError(ctx, *o)
	if err := e.service.revalidateCompletionSafety(ctx, o); err != nil {
		return err
	}
	if o.checkParentPrograms {
		// An occupied/stale parent cannot trigger a child pane closure either.
		if err := e.service.revalidateIntegrationTargetWithClean(ctx, o, true, "", false); err != nil {
			return err
		}
	}
	if err := completionPaneError(*o); err != nil {
		return err
	}
	paneID := effect.Details.Map()["pane"]
	if o.completionRuntime.CloseTaskPanes.Map()[paneID] != effect.Details.Map()["fingerprint"] {
		return staleBoundary("task pane close approval changed")
	}
	closer, ok := o.runtime.(runtime.PaneCloser)
	if !ok {
		return fmt.Errorf("runtime no longer supports exact pane closure")
	}
	if err := e.run(effect, func() (string, error) {
		if err := closer.ClosePane(ctx, paneID); err != nil {
			return "pane closure may be partial", err
		}
		return "closed task agent pane " + paneID, nil
	}); err != nil {
		e.partial = true
		return err
	}
	// Permit only the declared pane's disappearance, never unrelated runtime
	// changes. The next safety check also verifies final artifact/Git writes.
	expected := withoutOccupancyPane(o.occupancy, paneID)
	fresh, err := e.service.inspectOccupancy(ctx, o.runtime, o.checkout, runtime.OccupancyOptions{
		Profile: runtime.OccupancyStrict, CallerWorkspaceID: o.taskflowCallerWorkspace, CallerPaneID: o.taskflowCallerPane, InspectProcesses: true,
	})
	if err != nil {
		return err
	}
	if occupancyAuthority(expected, nil) != occupancyAuthority(fresh, nil) {
		return staleBoundary("runtime changed beyond the approved task pane closure")
	}
	approved := o.completionRuntime.CloseTaskPanes.Map()
	delete(approved, paneID)
	o.completionRuntime.CloseTaskPanes = NewFields(approved)
	o.occupancy, o.occupancyErr = fresh, nil
	return e.service.revalidateCompletionSafety(ctx, o)
}

func (s *lifecycleService) completionTaskClaimError(ctx context.Context, o lifecycleObservation) error {
	if len(o.completionRuntime.CloseTaskPanes.Entries()) == 0 {
		return nil
	}
	records, diagnostics, err := s.tasks.ListRecords()
	if err != nil || len(diagnostics) > 0 {
		return fmt.Errorf("task inventory is incomplete; task agent panes cannot be closed")
	}
	for _, record := range records {
		candidate := record.Task
		if candidate.ID == o.task.ID {
			continue
		}
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("task %s has invalid ownership metadata", candidate.ID)
		}
		if candidate.WorktreePath != "" {
			path, err := s.canonicalPath(candidate.WorktreePath)
			if err != nil {
				return fmt.Errorf("task %s checkout ownership is unknown", candidate.ID)
			}
			if path == o.checkout {
				return fmt.Errorf("task checkout is also claimed by task %s; preserve its panes and reconcile ownership", candidate.ID)
			}
		}
		if candidate.Branch == o.task.Branch {
			matches, complete, _ := s.taskRepositoryMatches(ctx, candidate, destructiveObservation{repoPath: o.repoPath, gitCommonDir: o.gitCommonDir})
			if !complete || matches {
				return fmt.Errorf("task %s also claims or may claim this branch; preserve its panes and reconcile ownership", candidate.ID)
			}
		}
	}
	return nil
}

func withoutOccupancyPane(o runtime.Occupancy, paneID string) runtime.Occupancy {
	out := o
	out.Agents = nil
	for _, a := range o.Agents {
		if a.Activity.PaneID != paneID {
			out.Agents = append(out.Agents, a)
		}
	}
	out.Sessions = nil
	for _, session := range o.Sessions {
		session.Runtime.Panes = withoutPane(session.Runtime.Panes, paneID)
		session.Panes = withoutPane(session.Panes, paneID)
		session.Mixed = withoutPane(session.Mixed, paneID)
		if len(session.Panes) > 0 {
			out.Sessions = append(out.Sessions, session)
		}
	}
	return out
}

func withoutPane(panes []runtime.Pane, id string) []runtime.Pane {
	var out []runtime.Pane
	for _, p := range panes {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return out
}
