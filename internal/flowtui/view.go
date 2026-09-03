package flowtui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

const wideLayoutWidth = 96

// View implements tea.Model. It performs no callback or probe work.
func (m Model) View() string {
	m.signalFirstView()
	if m.quitting {
		return ""
	}
	switch m.overlay {
	case overlayHelp:
		return m.renderHelp()
	case overlayRemoteMenu:
		return m.renderRemoteMenu()
	case overlayPlanLoading:
		return m.renderPlanLoading()
	case overlayPlan:
		return m.renderPlan(false)
	case overlayApplying:
		return m.renderPlan(true)
	case overlayResult:
		return m.renderResult()
	}
	if m.screen == screenPicker {
		return m.renderPicker()
	}
	return m.renderRepository()
}

func (m Model) renderPicker() string {
	var builder strings.Builder
	builder.WriteString(styleTitle.Render("dev flow  REPOSITORIES"))
	status := ""
	switch {
	case m.listRequest.loading:
		status = "LOADING"
	case m.listRequest.err != nil && m.listRequest.stale:
		status = "STALE"
	case m.listRequest.err != nil:
		status = "ERROR"
	}
	if status != "" {
		builder.WriteString("  [" + status + "]")
	}
	builder.WriteString("\n\n")
	if m.filterActive {
		builder.WriteString("filter: " + m.filter.View() + "\n\n")
	} else if m.filter.Value() != "" {
		builder.WriteString("filter: /" + m.filter.Value() + "\n\n")
	}
	if m.listRequest.err != nil {
		builder.WriteString(styleBad.Render("ERROR: "+m.listRequest.err.Error()) + "\n")
		if m.listRequest.stale {
			builder.WriteString("STALE: retaining the prior repository list; it is non-authoritative.\n")
		}
	}
	rows := m.filteredRepositories()
	if len(rows) == 0 {
		switch {
		case m.listRequest.loading:
			builder.WriteString("Loading local repositories asynchronously...\n")
		case m.listRequest.err != nil:
			builder.WriteString("No authoritative repository list is available.\n")
		case m.filter.Value() != "":
			builder.WriteString("No repository matches the current filter.\n")
		default:
			builder.WriteString("No repositories found.\n")
		}
	} else {
		for index, row := range rows {
			marker := "  "
			if index == m.repoCursor {
				marker = "> "
			}
			availability := "AVAILABLE"
			if !row.Available {
				availability = "UNAVAILABLE"
			}
			line := fmt.Sprintf("%s [%s] %s", marker, availability, repositoryLabel(row))
			if index == m.repoCursor {
				line = styleSelected.Render(line)
			}
			builder.WriteString(line + "\n")
			if row.Path != "" {
				builder.WriteString("    " + row.Path + "\n")
			}
			if row.Error != "" {
				builder.WriteString("    ERROR: " + row.Error + "\n")
			}
		}
	}
	builder.WriteString("\n")
	if m.status != "" {
		builder.WriteString("STATUS: " + m.status + "\n")
	}
	builder.WriteString(styleMuted.Render("j/k or arrows move | / filter | Enter select | r reload | ? help | q quit"))
	return builder.String()
}

func repositoryLabel(row RepositoryRow) string {
	if row.Name != "" {
		return row.Name
	}
	if row.Path != "" {
		return row.Path
	}
	return row.RepoKey
}

func (m Model) renderRepository() string {
	var builder strings.Builder
	name := repositoryLabel(m.repository)
	builder.WriteString(styleTitle.Render("dev flow  "+name) + "\n")
	if m.repository.Path != "" {
		builder.WriteString(m.repository.Path + "\n")
	}
	builder.WriteString(m.localStatusLine() + "\n")
	if m.hasSnapshot && !m.snapshot.ObservedAt.IsZero() {
		builder.WriteString("observed: " + formatTime(m.snapshot.ObservedAt) + "\n")
	}
	if m.repository.Error != "" {
		builder.WriteString("REPOSITORY ERROR: " + m.repository.Error + "\n")
	}
	if m.hasSnapshot && m.snapshot.Error != "" {
		builder.WriteString("SNAPSHOT ERROR: " + m.snapshot.Error + "\n")
	}
	if m.loadRequest.err != nil {
		builder.WriteString(styleBad.Render("REFRESH ERROR: "+m.loadRequest.err.Error()) + "\n")
	}
	builder.WriteString("\n")

	if !m.hasSnapshot {
		if m.loadRequest.loading {
			builder.WriteString("Loading repository topology and local observations asynchronously...\n")
		} else if m.loadRequest.err != nil {
			builder.WriteString("No authoritative repository snapshot is available.\n")
		} else {
			builder.WriteString("No repository snapshot is available.\n")
		}
		builder.WriteString("\n" + m.renderRepositoryFooter())
		return builder.String()
	}

	left := m.surfacePanelLines()
	middle := m.lifecyclePanelLines()
	right := m.actionPanelLines()
	if m.width >= wideLayoutWidth {
		builder.WriteString(renderWidePanels(
			panelTitle("SURFACES", m.focus == FocusSurfaces), left,
			panelTitle("LIFECYCLE / EVIDENCE", m.focus == FocusLifecycle), middle,
			panelTitle("ACTIONS / CONDITION", m.focus == FocusActions), right,
			m.width,
		))
	} else {
		builder.WriteString(renderNarrowPanel(panelTitle("SURFACES", m.focus == FocusSurfaces), left))
		builder.WriteString("\n")
		builder.WriteString(renderNarrowPanel(panelTitle("LIFECYCLE / EVIDENCE", m.focus == FocusLifecycle), middle))
		builder.WriteString("\n")
		builder.WriteString(renderNarrowPanel(panelTitle("ACTIONS / CONDITION", m.focus == FocusActions), right))
	}
	builder.WriteString("\n\n" + m.renderRepositoryFooter())
	return builder.String()
}

func (m Model) localStatusLine() string {
	label := "UNKNOWN"
	if m.hasSnapshot {
		label = strings.ToUpper(string(m.snapshot.Freshness))
	}
	if m.loadRequest.loading {
		label = "REFRESHING"
	}
	authoritative := m.hasSnapshot && m.snapshot.Authoritative() && !m.loadRequest.loading
	if !authoritative {
		label += " | NON-AUTHORITATIVE"
	}
	if m.loadRequest.stale {
		label += " | STALE"
	}
	if m.hasSnapshot && !m.snapshot.Repository.Available {
		label += " | UNAVAILABLE"
	}
	return "LOCAL: " + label
}

func panelTitle(title string, focused bool) string {
	if focused {
		return title + " [FOCUS]"
	}
	return title
}

func (m Model) surfacePanelLines() []string {
	rows := m.snapshot.Surfaces.Values()
	if len(rows) == 0 {
		return []string{"(valid empty snapshot)", "No checkout or task-only rows."}
	}
	lines := make([]string, 0, len(rows)*2)
	for index, row := range rows {
		marker := "  "
		if index == m.rowCursor {
			marker = "> "
		}
		badges := []string{"[" + strings.ToUpper(string(row.Kind)) + "]"}
		if len(row.Drift.Values()) > 0 {
			badges = append(badges, "[DRIFT]")
		}
		if len(row.Conflicts.Values()) > 0 || row.Kind == SurfaceConflict {
			badges = append(badges, "[CONFLICT]")
		}
		label := row.Label
		if label == "" {
			label = row.Branch
		}
		if label == "" {
			label = row.RowKey
		}
		line := marker + label + " " + strings.Join(badges, " ")
		if index == m.rowCursor {
			line = styleSelected.Render(line)
		}
		lines = append(lines, line)
		if row.Path != "" {
			lines = append(lines, "    "+row.Path)
		} else if row.Locator.TaskID != "" {
			lines = append(lines, "    task "+row.Locator.TaskID+" (no checkout)")
		}
	}
	return lines
}

func (m Model) lifecyclePanelLines() []string {
	row, ok := m.currentSurface()
	if !ok {
		return []string{"No surface selected."}
	}
	lines := []string{"kind: " + strings.ToUpper(string(row.Kind))}
	if row.Mode == "" || row.State == "" {
		lines = append(lines, "recorded lifecycle: UNTRACKED")
	} else {
		lines = append(lines,
			"mode: "+strings.ToUpper(string(row.Mode)),
			"rail: "+lifecycleRail(row.Mode, row.State),
			"current persisted state: "+strings.ToUpper(string(row.State)),
		)
	}
	if row.Branch != "" {
		lines = append(lines, "branch: "+row.Branch)
	}
	if row.Base != "" {
		lines = append(lines, "base: "+row.Base)
	}
	for _, evidence := range row.Evidence.Values() {
		lines = append(lines, "EVIDENCE: "+evidence)
	}
	for _, drift := range row.Drift.Values() {
		lines = append(lines, "DRIFT: "+drift)
	}
	for _, conflict := range row.Conflicts.Values() {
		lines = append(lines, "CONFLICT: "+conflict)
	}
	if remote, ok := m.remote.RemoteObservation(); ok {
		lines = append(lines, remoteObservationLines(remote)...)
	}
	return lines
}

func lifecycleRail(mode task.CheckoutMode, current task.State) string {
	states := []task.State{task.Hot, task.Warm, task.Cold, task.Done}
	if mode == task.ModeDirect {
		states = []task.State{task.Hot, task.Warm, task.Done}
	}
	parts := make([]string, 0, len(states))
	for _, state := range states {
		label := strings.ToUpper(string(state))
		if state == current {
			label = "[" + label + " CURRENT]"
		}
		parts = append(parts, colourState(state, label))
	}
	return strings.Join(parts, " -> ")
}

func colourState(state task.State, value string) string {
	switch state {
	case task.Hot:
		return styleHot.Render(value)
	case task.Warm:
		return styleWarm.Render(value)
	case task.Cold:
		return styleCold.Render(value)
	case task.Done:
		return styleDone.Render(value)
	default:
		return value
	}
}

func (m Model) actionPanelLines() []string {
	row, ok := m.currentSurface()
	if !ok {
		return []string{"No surface selected."}
	}
	actions := row.Actions.Values()
	if len(actions) == 0 {
		return []string{"No guarded actions are available.", "This row is inspect-only."}
	}
	lines := make([]string, 0, len(actions)+4)
	for index, choice := range actions {
		marker := "  "
		if index == m.actionCursor {
			marker = "> "
		}
		label := choice.Label
		if !choice.Valid() {
			label += " [INVALID]"
		}
		line := marker + label
		if index == m.actionCursor {
			line = styleSelected.Render(line)
		}
		lines = append(lines, line)
	}
	if choice, ok := m.currentAction(); ok {
		lines = append(lines, "", "selected: "+choice.Description)
		lines = append(lines, "Enter: inspect a fresh plan; no immediate mutation.")
		if choice.Action() == taskflow.RefreshRemote {
			lines = append(lines, "network variant: "+remoteChoiceDescription(choice.Options()))
		}
	}
	return lines
}

func remoteChoiceDescription(options taskflow.ActionOptions) string {
	remote, ok := options.(taskflow.RefreshRemoteOptions)
	if !ok {
		return "invalid remote options"
	}
	switch {
	case remote.FetchRefs && remote.QueryReview:
		return "FETCH REFS + QUERY REVIEW"
	case remote.FetchRefs:
		return "FETCH REFS"
	case remote.QueryReview:
		return "QUERY REVIEW"
	default:
		return "NO OPERATION SELECTED"
	}
}

func (m Model) renderRepositoryFooter() string {
	var lines []string
	if m.status != "" {
		lines = append(lines, "STATUS: "+m.status)
	}
	lines = append(lines,
		"j/k or arrows row | h/l or arrows action | Tab focus | Enter plan",
		"r local reload | R remote menu | Esc repositories | ? help | q quit",
	)
	return styleMuted.Render(strings.Join(lines, "\n"))
}

func renderWidePanels(leftTitle string, left []string, middleTitle string, middle []string, rightTitle string, right []string, width int) string {
	gutter := " | "
	usable := width - 2*lipgloss.Width(gutter)
	if usable < 60 {
		usable = 60
	}
	leftWidth := usable * 30 / 100
	middleWidth := usable * 36 / 100
	rightWidth := usable - leftWidth - middleWidth
	left = append([]string{leftTitle, strings.Repeat("-", max(1, leftWidth))}, left...)
	middle = append([]string{middleTitle, strings.Repeat("-", max(1, middleWidth))}, middle...)
	right = append([]string{rightTitle, strings.Repeat("-", max(1, rightWidth))}, right...)
	count := max(len(left), len(middle), len(right))
	var builder strings.Builder
	for index := 0; index < count; index++ {
		builder.WriteString(fitPanelLine(lineAt(left, index), leftWidth))
		builder.WriteString(gutter)
		builder.WriteString(fitPanelLine(lineAt(middle, index), middleWidth))
		builder.WriteString(gutter)
		builder.WriteString(fitPanelLine(lineAt(right, index), rightWidth))
		if index+1 < count {
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func renderNarrowPanel(title string, lines []string) string {
	return title + "\n" + strings.Repeat("-", max(1, lipgloss.Width(title))) + "\n" + strings.Join(lines, "\n")
}

func lineAt(lines []string, index int) string {
	if index < 0 || index >= len(lines) {
		return ""
	}
	return lines[index]
}

func fitPanelLine(value string, width int) string {
	value = truncateDisplay(value, width)
	missing := width - lipgloss.Width(value)
	if missing > 0 {
		value += strings.Repeat(" ", missing)
	}
	return value
}

func truncateDisplay(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(value, width, "…")
}

func (m Model) renderHelp() string {
	return strings.Join([]string{
		styleTitle.Render("dev flow  HELP"),
		"",
		"MODEL",
		"  HOT / WARM / COLD / DONE are the only persisted task states.",
		"  READY is plan availability; REVIEW / MERGED / RETIRED are result milestones only.",
		"  DRIFT and CONFLICT are evidence labels, never mutation permission.",
		"  STALE and ERROR observations are non-authoritative.",
		"",
		"KEYS",
		"  j/k or up/down     select surface (or repository/menu row)",
		"  h/l or left/right  select an already-concrete action variant",
		"  Tab                move panel focus",
		"  /                  filter the repository picker",
		"  Enter              call Plan and inspect it; never mutate immediately",
		"  y                  approve a READY non-typed plan",
		"  r                  reload local facts only",
		"  R                  choose provided fetch/query/both remote variants",
		"  Esc                back out",
		"  q                  quit (queued while Apply is running)",
		"",
		styleMuted.Render("? or Esc closes help | q quits"),
	}, "\n")
}

func (m Model) renderRemoteMenu() string {
	var builder strings.Builder
	builder.WriteString(styleTitle.Render("dev flow  REMOTE ACTIONS") + "\n\n")
	builder.WriteString("Remote work is never part of startup or local reload. Choose a caller-provided concrete option:\n\n")
	for index, choice := range m.remoteChoices.Values() {
		marker := "  "
		if index == m.remoteCursor {
			marker = "> "
		}
		line := marker + choice.Label + " [" + remoteChoiceDescription(choice.Options()) + "]"
		if index == m.remoteCursor {
			line = styleSelected.Render(line)
		}
		builder.WriteString(line + "\n")
		if choice.Description != "" {
			builder.WriteString("    " + choice.Description + "\n")
		}
	}
	builder.WriteString("\n" + styleMuted.Render("j/k choose | Enter plan | R or Esc back | q quit"))
	return builder.String()
}

func (m Model) renderPlanLoading() string {
	return strings.Join([]string{
		styleTitle.Render("dev flow  PLAN"),
		"",
		"Planning " + m.planTarget.choice.Label + " for exact row " + m.planTarget.rowKey + "...",
		"No mutation has started.",
		"",
		styleMuted.Render("Esc cancel planning | q quit"),
	}, "\n")
}

func (m Model) renderPlan(applying bool) string {
	var builder strings.Builder
	title := "dev flow  PLAN"
	if applying {
		title = "dev flow  APPLYING"
	}
	builder.WriteString(styleTitle.Render(title) + "\n\n")
	builder.WriteString("repository: " + m.planTarget.repoKey + "\n")
	builder.WriteString("row: " + m.planTarget.rowKey + "\n")
	builder.WriteString("choice: " + m.planTarget.actionID + " — " + m.planTarget.choice.Label + "\n")
	if m.planErr != nil {
		builder.WriteString("\n" + styleBad.Render("PLAN ERROR: "+m.planErr.Error()) + "\n")
	}
	if !m.hasPlan {
		builder.WriteString("\nNo valid plan is available. This error is inspectable and has no Apply path.\n")
		builder.WriteString("\n" + styleMuted.Render("Esc back | q quit"))
		return builder.String()
	}

	builder.WriteString("plan ID: " + m.plan.PlanID + "\n")
	builder.WriteString("availability: " + availabilityLabel(m.plan.Availability) + "\n")
	if m.plan.Summary != "" {
		builder.WriteString("summary: " + m.plan.Summary + "\n")
	}
	if m.plan.HasPersistedTransition {
		target := strings.ToUpper(string(m.plan.Target))
		if m.plan.RemovesTask {
			target = "TASK REMOVED"
		}
		builder.WriteString("persisted transition: " + strings.ToUpper(string(m.plan.Source)) + " -> " + target)
		if m.plan.StatePreserving {
			builder.WriteString(" (state-preserving)")
		}
		builder.WriteString("\n")
	}
	if milestone := milestoneLabel(m.plan.ExpectedMilestone); milestone != "" {
		builder.WriteString("expected result milestone: " + milestone + " (not a persisted state)\n")
	}

	builder.WriteString("\nCONDITIONS (ordered)\n")
	conditions := m.plan.Conditions()
	if len(conditions) == 0 {
		builder.WriteString("  (none)\n")
	}
	for index, condition := range conditions {
		builder.WriteString(fmt.Sprintf("  %d. [%s] [%s] %s\n", index+1,
			availabilityLabel(taskflow.Availability(condition.Verdict)), strings.ToUpper(string(condition.Requirement)), condition.Code))
		evidence := condition.Evidence
		if evidence == "" {
			evidence = "(none recorded)"
		}
		builder.WriteString("     evidence: " + evidence + "\n")
		remediation := condition.Remediation
		if remediation == "" {
			remediation = "(none required)"
		}
		builder.WriteString("     remediation: " + remediation + "\n")
	}

	builder.WriteString("\nEFFECTS (execution order)\n")
	effects := m.plan.Effects()
	if len(effects) == 0 {
		builder.WriteString("  (none)\n")
	}
	for index, effect := range effects {
		markers := []string{}
		if effect.Network {
			markers = append(markers, "NETWORK")
		}
		if effect.Destructive {
			markers = append(markers, "DESTRUCTIVE")
		}
		markerText := ""
		if len(markers) > 0 {
			markerText = " [" + strings.Join(markers, "] [") + "]"
		}
		builder.WriteString(fmt.Sprintf("  %d. %s%s — %s", index+1, effect.Code, markerText, effect.Description))
		if effect.Target != "" {
			builder.WriteString(" -> " + effect.Target)
		}
		builder.WriteString("\n")
		for _, detail := range effect.Details.Entries() {
			builder.WriteString("     " + detail.Key + ": " + detail.Value + "\n")
		}
	}

	builder.WriteString("\nRETAINED RESOURCES\n")
	retained := m.plan.RetainedResources()
	if len(retained) == 0 {
		builder.WriteString("  (none)\n")
	} else {
		for _, resource := range retained {
			builder.WriteString("  - " + resource + "\n")
		}
	}
	builder.WriteString("fallback command: ")
	if m.plan.FallbackCommand == "" {
		builder.WriteString("(none supplied)\n")
	} else {
		builder.WriteString(m.plan.FallbackCommand + "\n")
	}

	builder.WriteString("\n")
	switch {
	case applying:
		builder.WriteString("APPLYING: conflicting navigation and refresh are disabled. q/r are queued.\n")
	case !m.planCanApply():
		builder.WriteString("NO APPLY PATH: only READY plans can be approved; this plan remains inspectable.\n")
	case m.plan.Confirmation.Kind == taskflow.ConfirmationTyped:
		prompt := m.plan.Confirmation.Prompt
		if prompt == "" {
			prompt = "Type the exact confirmation token"
		}
		builder.WriteString(prompt + "\n")
		builder.WriteString("required exact token: " + m.plan.Confirmation.Token + "\n")
		builder.WriteString("confirmation: " + m.confirm.View() + "\n")
		builder.WriteString("Enter validates and applies; any mismatch remains non-mutating.\n")
	default:
		prompt := m.plan.Confirmation.Prompt
		if prompt != "" {
			builder.WriteString(prompt + "\n")
		}
		builder.WriteString("Press y to approve this exact PlanID. Enter never applies.\n")
	}
	if m.status != "" {
		builder.WriteString("STATUS: " + m.status + "\n")
	}
	if !applying {
		builder.WriteString(styleMuted.Render("Esc back | q quit"))
	}
	return builder.String()
}

func availabilityLabel(value taskflow.Availability) string {
	if value == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(strings.ReplaceAll(string(value), "-", " "))
}

func milestoneLabel(value taskflow.Milestone) string {
	switch value {
	case taskflow.MilestoneReviewReady:
		return "REVIEW"
	case taskflow.MilestoneMerged:
		return "MERGED"
	case taskflow.MilestoneRetired:
		return "RETIRED"
	case taskflow.MilestoneAdopted:
		return "ADOPTED"
	case taskflow.MilestoneReconciled:
		return "RECONCILED"
	default:
		return ""
	}
}

func (m Model) renderResult() string {
	var builder strings.Builder
	builder.WriteString(styleTitle.Render("dev flow  RESULT") + "\n\n")
	builder.WriteString("choice: " + m.planTarget.actionID + " — " + m.planTarget.choice.Label + "\n")
	if m.lastResultErr != nil {
		builder.WriteString(styleBad.Render("APPLY ERROR: "+m.lastResultErr.Error()) + "\n")
	} else {
		builder.WriteString(styleGood.Render("APPLY COMPLETED") + "\n")
	}
	if m.lastResult.PartialSuccess {
		builder.WriteString("PARTIAL SUCCESS: completed effects remain completed; no rollback is implied.\n")
	}
	if milestone := milestoneLabel(m.lastResult.Milestone); milestone != "" {
		builder.WriteString("result milestone: " + milestone + " (not a persisted state)\n")
	}

	builder.WriteString("\nSTEP LEDGER\n")
	steps := m.lastResult.AttemptedSteps()
	if len(steps) == 0 {
		builder.WriteString("  (no effect was attempted)\n")
	}
	for index, step := range steps {
		builder.WriteString(fmt.Sprintf("  %d. [%s] %s — %s", index+1,
			strings.ToUpper(string(step.Status)), step.Effect.Code, step.Effect.Description))
		if step.Detail != "" {
			builder.WriteString(" | " + step.Detail)
		}
		if step.Failure != "" {
			builder.WriteString(" | FAILURE: " + step.Failure)
		}
		builder.WriteString("\n")
	}
	builder.WriteString("\nWARNINGS\n")
	writeStringSection(&builder, m.lastResult.Warnings())
	builder.WriteString("RECOVERY\n")
	writeStringSection(&builder, m.lastResult.Recovery())
	if remote, ok := m.lastResult.RemoteObservation(); ok {
		builder.WriteString("REMOTE OBSERVATION (run-local)\n")
		for _, line := range remoteObservationLines(remote) {
			builder.WriteString("  " + line + "\n")
		}
	}
	if handoff, ok := m.lastResult.Handoff(); ok {
		builder.WriteString("HANDOFF (available after alternate-screen exit)\n")
		builder.WriteString("  " + handoffDescription(handoff) + "\n")
	}
	if m.loadRequest.loading {
		builder.WriteString("\nREFRESHING: a new local generation is loading; state was not changed optimistically.\n")
	} else if m.loadRequest.err != nil {
		builder.WriteString("\nSTALE: post-Apply local reload failed: " + m.loadRequest.err.Error() + "\n")
	}
	if m.status != "" {
		builder.WriteString("STATUS: " + m.status + "\n")
	}
	builder.WriteString("\n" + styleMuted.Render("Esc return to repository | r reload again | ? help | q quit"))
	return builder.String()
}

func writeStringSection(builder *strings.Builder, values []string) {
	if len(values) == 0 {
		builder.WriteString("  (none)\n")
		return
	}
	for _, value := range values {
		builder.WriteString("  - " + value + "\n")
	}
}

func handoffDescription(handoff taskflow.Handoff) string {
	parts := []string{strings.ToUpper(string(handoff.Kind))}
	for _, value := range []string{handoff.Path, handoff.Runtime, handoff.RuntimeHandle, handoff.URL, handoff.Label} {
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " | ")
}

func remoteObservationLines(remote taskflow.RemoteObservation) []string {
	lines := []string{"REMOTE: " + remote.RemoteName}
	if remote.Provider != "" {
		lines = append(lines, "REMOTE PROVIDER: "+strings.ToUpper(string(remote.Provider)))
	}
	if remote.Repository != "" {
		lines = append(lines, "REMOTE REPOSITORY: "+remote.Repository)
	}
	if remote.Head != "" || remote.Base != "" {
		lines = append(lines, "REMOTE BRANCHES: "+remote.Head+" -> "+remote.Base)
	}
	lines = append(lines, "REMOTE BEFORE REFS: "+refsSummary(remote.BeforeRefs))
	if !remote.BeforeRefs.ObservedAt.IsZero() {
		lines = append(lines, "REMOTE BEFORE OBSERVED: "+formatTime(remote.BeforeRefs.ObservedAt))
	}
	if remote.HasAfterRefs {
		lines = append(lines, "REMOTE AFTER REFS: "+refsSummary(remote.AfterRefs))
		if !remote.AfterRefs.ObservedAt.IsZero() {
			lines = append(lines, "REMOTE AFTER OBSERVED: "+formatTime(remote.AfterRefs.ObservedAt))
		}
	}
	if remote.HasReview {
		review := remote.Review
		switch {
		case review.State == taskflow.ObservationError:
			lines = append(lines, "REMOTE REVIEW ERROR: "+string(review.FailureKind)+" — "+review.Failure)
		case review.State == taskflow.ObservationKnown && !review.Exists:
			lines = append(lines, "REMOTE REVIEW: NONE (known)")
		case review.State == taskflow.ObservationKnown:
			lines = append(lines, fmt.Sprintf("REMOTE REVIEW: %s %s", strings.ToUpper(string(review.ReviewState)), review.URL))
		default:
			lines = append(lines, "REMOTE REVIEW: "+strings.ToUpper(string(review.State)))
		}
		if !review.ObservedAt.IsZero() {
			lines = append(lines, "REMOTE REVIEW OBSERVED: "+formatTime(review.ObservedAt))
		}
	}
	return lines
}

func refsSummary(refs taskflow.RemoteRefsObservation) string {
	parts := make([]string, 0, 4)
	for _, ref := range []taskflow.NamedRefObservation{refs.LocalHead, refs.LocalBase, refs.RemoteHead, refs.RemoteBase} {
		if ref.Ref == "" {
			continue
		}
		switch {
		case ref.Failure != "":
			parts = append(parts, ref.Ref+"=ERROR("+ref.Failure+")")
		case ref.Exists:
			parts = append(parts, ref.Ref+"="+ref.OID)
		default:
			parts = append(parts, ref.Ref+"=ABSENT")
		}
	}
	if len(parts) == 0 {
		return "no named refs observed"
	}
	return strings.Join(parts, "; ")
}

func formatTime(value time.Time) string {
	return value.Local().Format("2006-01-02 15:04:05 MST")
}
