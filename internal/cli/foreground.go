package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
)

const foregroundLimit = "Foreground inspection is shown per pane. Background jobs were not inspected; closing terminals may also stop them."

func doneOccupancy(ctx context.Context, app *App, selected task.Task, checkout string) (runtime.Occupancy, error) {
	rt := runtimeForTask(app, &selected)
	caller, err := callerPaneID(ctx, rt)
	if err != nil {
		return runtime.Occupancy{}, err
	}
	return runtime.InspectOccupancy(ctx, rt, checkout, runtime.OccupancyOptions{
		Profile: runtime.OccupancyStrict, CallerPaneID: caller, CallerWorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"), InspectProcesses: true,
	})
}

// confirmRetirement is shared by done and interactive retire. Selecting
// permissions never mutates a terminal; only the final guarded Apply does.
func confirmRetirement(ctx context.Context, app *App, p *prompter, rt runtime.Runtime, preview retire.Inspection, options flow.RetireOptions) (flow.RetireOptions, retire.Inspection, bool, error) {
	approved, canceled, err := confirmRetirementPrograms(app, p, preview)
	if err != nil || canceled {
		return options, preview, canceled, err
	}
	options.ProcessClosures = flow.NewFields(approved)
	if len(preview.UnknownSessions) > 0 && !options.CloseUnknown {
		confirmed, err := p.confirm(fmt.Sprintf("Close the listed %d workspace(s) with unknown agent/runtime status?", len(preview.UnknownSessions)), false)
		if errors.Is(err, errPromptCanceled) || !confirmed {
			fmt.Fprintln(app.Out, "Cleanup kept; unknown runtime status was not authorized.")
			return options, preview, true, nil
		}
		if err != nil {
			return options, preview, false, err
		}
		options.CloseUnknown = true
	}
	fresh, err := retire.InspectForExternalCoordinator(ctx, rt, preview.Target, retire.Options{
		CloseUnknown: options.CloseUnknown, AssumeNoRuntime: options.AssumeNoRuntime, ProcessClosures: approved,
	})
	if err != nil {
		return options, preview, false, err
	}
	if fresh.Fingerprint() != preview.Fingerprint() {
		return options, fresh, false, errors.New("retirement preview is stale: panes or foreground programs changed; refresh before cleanup")
	}
	if !fresh.Ready() {
		return options, fresh, false, fmt.Errorf("retirement is blocked: %s", strings.Join(fresh.Blockers, "; "))
	}
	options.RuntimeFingerprint = fresh.Fingerprint()
	fmt.Fprintf(app.Out, "  REMOVE worktree %s after runtime release\n", config.Contract(fresh.Target))
	if options.DeleteBranch {
		fmt.Fprintln(app.Out, "  DELETE its local branch only after fresh containment verification (requested)")
	} else {
		fmt.Fprintln(app.Out, "  KEEP local branch")
	}
	if len(fresh.Sessions) > 0 {
		confirmed, err := p.confirm(fmt.Sprintf("Close ALL listed panes in these %d target workspace(s) and retire checkout %s? Parent and other workspaces stay open", len(fresh.Sessions), config.Contract(fresh.Target)), false)
		if errors.Is(err, errPromptCanceled) || !confirmed {
			fmt.Fprintln(app.Out, "Cleanup canceled; no runtime workspace was closed by this cleanup attempt.")
			return options, fresh, true, nil
		}
		if err != nil {
			return options, fresh, false, err
		}
	}
	return options, fresh, false, nil
}

func renderForegroundPane(app *App, pane runtime.Pane) {
	tab := pane.TabID
	if tab == "" {
		tab = "unknown"
	}
	fmt.Fprintf(app.Out, "    tab %s / pane %s · %s\n", runtime.DisplayText(tab), pane.ID, runtime.DisplayText(config.Contract(pane.CWD)))
	if pane.Agent != "" || pane.AgentSession != "" {
		fmt.Fprintf(app.Out, "      agent %s · %s", runtime.DisplayText(pane.Agent), runtime.DisplayText(pane.AgentStatus))
		if pane.AgentName != "" {
			fmt.Fprintf(app.Out, " · %s", runtime.DisplayText(pane.AgentName))
		}
		fmt.Fprintln(app.Out)
	}
	if pane.Process == nil {
		fmt.Fprintln(app.Out, "      foreground programs: unknown (runtime capability unavailable)")
		return
	}
	switch pane.Process.State {
	case runtime.ProcessShell:
		fmt.Fprintln(app.Out, "      foreground shell (not proof of no background jobs)")
	case runtime.ProcessCaller:
		fmt.Fprintln(app.Out, "      this dev command · caller; coordinator must wait for foreground shell")
	case runtime.ProcessUnknown:
		fmt.Fprintf(app.Out, "      foreground programs: unknown · %s\n", runtime.DisplayText(pane.Process.Error))
	}
	for _, process := range pane.Process.Processes {
		fmt.Fprintf(app.Out, "      %s · PID %d · %s\n", runtime.DisplayText(process.Name), process.PID, runtime.DisplayText(config.Contract(process.CWD)))
	}
}

func renderForegroundOccupancy(app *App, occupancy runtime.Occupancy, disposition string) {
	sessions := append([]runtime.OccupancySession(nil), occupancy.Sessions...)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Runtime.Handle < sessions[j].Runtime.Handle })
	for _, session := range sessions {
		fmt.Fprintf(app.Out, "  %s workspace %s (%s)\n", disposition, session.Runtime.Handle, runtime.DisplayText(session.Runtime.Label))
		for _, pane := range session.Panes {
			renderForegroundPane(app, pane)
		}
	}
}

func confirmDoneForegroundPrograms(ctx context.Context, app *App, p *prompter, selected task.Task, plan flow.Plan) (map[string]string, bool, error) {
	consent := make(map[string]string)
	paths := []string{plan.Locator.CheckoutPath}
	if donePlanHasEffect(plan, flow.EffectMergeFF) && plan.Locator.RepoPath != plan.Locator.CheckoutPath {
		paths = append(paths, plan.Locator.RepoPath)
	}
	for _, path := range paths {
		occupancy, err := doneOccupancy(ctx, app, selected, path)
		if err != nil {
			return nil, false, err
		}
		fingerprint, err := runtime.ForegroundFingerprint(occupancy)
		if err != nil {
			renderForegroundOccupancy(app, occupancy, "KEEP")
			return nil, false, err
		}
		if fingerprint == "" {
			continue
		}
		fmt.Fprintf(app.Out, "\nForeground programs · checkout %s\n", config.Contract(path))
		renderForegroundOccupancy(app, occupancy, "KEEP")
		fmt.Fprintln(app.Out, foregroundLimit)
		confirmed, err := p.confirm("FF will update files in this checkout. The listed programs will remain running. Continue?", false)
		if canceledDone(app, err, !confirmed) {
			return nil, true, nil
		}
		if err != nil {
			return nil, false, err
		}
		consent[occupancy.Target] = fingerprint
	}
	return consent, false, nil
}

// confirmRetirementPrograms authorizes only exact non-agent processes in
// eligible target workspaces, separately from unknown agent acknowledgements.
func confirmRetirementPrograms(app *App, p *prompter, preview retire.Inspection) (map[string]string, bool, error) {
	approved := make(map[string]string)
	for _, session := range preview.Sessions {
		programs := retire.ProcessClosuresFor(session)
		if len(programs) == 0 {
			continue
		}
		if session.Protection != "" || len(session.Mixed) > 0 {
			continue
		}
		fmt.Fprintf(app.Out, "\nClosing workspace %s will terminate these programs and all listed panes:\n", session.Runtime.Handle)
		for _, pane := range session.Panes {
			renderForegroundPane(app, pane)
		}
		fmt.Fprintln(app.Out, foregroundLimit)
		token := "CLOSE " + session.Runtime.Handle
		value, err := p.dangerLine("Type " + token + " to authorize termination; this does not close anything until final retirement approval")
		if errors.Is(err, errPromptCanceled) || value != token {
			fmt.Fprintln(app.Out, "Cleanup canceled; no workspace was closed by this cleanup attempt.")
			return nil, true, nil
		}
		if err != nil {
			return nil, false, err
		}
		for id, fingerprint := range programs {
			approved[id] = fingerprint
		}
	}
	return approved, false, nil
}
