// Package retire coordinates destructive cleanup from outside the runtime and
// worktree being retired.
package retire

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

// Options controls only fail-closed observations. Caller protection and active
// agent states are never bypassable.
type Options struct {
	CWD               string
	CallerWorkspaceID string
	CallerPaneID      string
	CloseUnknown      bool
	AssumeNoRuntime   bool
	PollInterval      time.Duration
	Timeout           time.Duration
}

// Session is one runtime session that currently covers the target checkout.
type Session struct {
	Runtime runtime.Session
	Panes   []runtime.Pane
	Mixed   []runtime.Pane
}

// Inspection is a fresh view of every runtime surface covering a checkout.
type Inspection struct {
	Target          string
	Sessions        []Session
	ClosedSessions  int
	Blockers        []string
	RuntimeUnknown  bool
	CallerContained bool
}

// Ready reports whether destructive cleanup may proceed.
func (i Inspection) Ready() bool { return len(i.Blockers) == 0 }

// Inspect discovers covering sessions and applies caller, mixed-workspace, and
// agent-state safety policy.
func Inspect(ctx context.Context, rt runtime.Runtime, target string, opts Options) (Inspection, error) {
	runtimeName := ""
	if rt != nil {
		runtimeName = rt.Name()
	}
	if opts.CallerWorkspaceID == "" && runtimeName != "tmux" && runtimeName != "zellij" && runtimeName != "none" {
		opts.CallerWorkspaceID = os.Getenv("HERDR_WORKSPACE_ID")
	}
	if opts.CallerPaneID == "" {
		switch runtimeName {
		case "herdr":
			opts.CallerPaneID = os.Getenv("HERDR_PANE_ID")
		case "tmux":
			opts.CallerPaneID = os.Getenv("TMUX_PANE")
		case "zellij", "none":
		default:
			opts.CallerPaneID = os.Getenv("HERDR_PANE_ID")
			if opts.CallerPaneID == "" {
				opts.CallerPaneID = os.Getenv("TMUX_PANE")
			}
		}
	}
	evidence, err := runtime.InspectOccupancy(ctx, rt, target, runtime.OccupancyOptions{
		Profile:           runtime.OccupancyCleanup,
		CallerWorkspaceID: opts.CallerWorkspaceID,
		CallerPaneID:      opts.CallerPaneID,
		CloseUnknown:      opts.CloseUnknown,
	})
	if err != nil {
		return Inspection{}, fmt.Errorf("inspect retirement occupancy: %w", err)
	}
	result := Inspection{Target: evidence.Target}
	cwd := opts.CWD
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return Inspection{}, fmt.Errorf("resolve caller directory: %w", err)
		}
	}
	if inside, compareErr := pathx.Contains(evidence.Target, cwd); compareErr != nil {
		return Inspection{}, fmt.Errorf("compare caller directory: %w", compareErr)
	} else if inside {
		result.CallerContained = true
		result.Blockers = append(result.Blockers,
			fmt.Sprintf("caller is inside target checkout %s", evidence.Target))
	}

	if evidence.CurrentPane.Err != nil {
		return Inspection{}, fmt.Errorf("resolve current %s runtime pane: %w", evidence.Backend, evidence.CurrentPane.Err)
	}
	if evidence.SessionCoverageErr != nil {
		return Inspection{}, evidence.SessionCoverageErr
	}
	if !evidence.SessionList.Supported {
		result.RuntimeUnknown = true
		if !opts.AssumeNoRuntime {
			result.Blockers = append(result.Blockers,
				fmt.Sprintf("runtime backend %s cannot enumerate covering sessions; use the explicit assume-no-runtime acknowledgement from an external caller", evidence.Backend))
		}
		return dedupe(result), nil
	}
	if evidence.SessionList.Err != nil {
		if !opts.AssumeNoRuntime {
			return Inspection{}, fmt.Errorf("list %s runtime sessions: %w", evidence.Backend, evidence.SessionList.Err)
		}
		result.RuntimeUnknown = true
	}
	if evidence.AgentActivityList.Err != nil {
		return Inspection{}, fmt.Errorf("list %s recognized agents: %w", evidence.Backend, evidence.AgentActivityList.Err)
	}
	for _, observed := range evidence.Sessions {
		result.Sessions = append(result.Sessions, Session{
			Runtime: observed.Runtime,
			Panes:   observed.Panes,
			Mixed:   observed.Mixed,
		})
		if observed.IsCaller {
			result.CallerContained = true
			result.Blockers = append(result.Blockers,
				fmt.Sprintf("caller runtime %s contains the target checkout", observed.Runtime.Handle))
		}
		if len(observed.Mixed) > 0 {
			result.Blockers = append(result.Blockers,
				fmt.Sprintf("runtime %s also contains %d pane(s) outside the target", observed.Runtime.Handle, len(observed.Mixed)))
		}
		for _, status := range coveringAgentStatuses(observed.Panes, observed.Runtime.AgentStatus) {
			appendAgentStatusBlocker(&result, observed.Runtime.Handle, status, opts.CloseUnknown)
		}
	}
	for _, agent := range evidence.Agents {
		if !agent.Blocking {
			continue
		}
		if agent.IsCaller {
			result.CallerContained = true
			if agent.SessionHandle != "" {
				result.Blockers = append(result.Blockers,
					fmt.Sprintf("caller runtime %s contains the target checkout", agent.SessionHandle))
			} else {
				result.Blockers = append(result.Blockers,
					fmt.Sprintf("caller runtime pane %s contains the target checkout", agent.Activity.PaneID))
			}
			continue
		}
		if agent.SessionHandle == "" {
			result.Blockers = append(result.Blockers,
				fmt.Sprintf("recognized agent in pane %s covers the target without a closeable runtime session", agent.Activity.PaneID))
			continue
		}
		appendAgentStatusBlocker(&result, agent.SessionHandle, agent.Status, opts.CloseUnknown)
	}
	return dedupe(result), nil
}

// CloseAndWait closes every eligible covering session and proves that no
// runtime surface still covers target before returning.
func CloseAndWait(ctx context.Context, rt runtime.Runtime, target string, opts Options) (Inspection, error) {
	inspection, err := Inspect(ctx, rt, target, opts)
	if err != nil {
		return inspection, err
	}
	if !inspection.Ready() {
		return inspection, fmt.Errorf("retirement blocked: %s", strings.Join(inspection.Blockers, "; "))
	}
	if inspection.RuntimeUnknown || rt == nil || rt.Name() == "none" {
		return inspection, nil
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	closed := make(map[string]bool)
	for {
		fresh, inspectErr := Inspect(ctx, rt, inspection.Target, opts)
		if inspectErr != nil {
			return inspection, fmt.Errorf("reinspect before %s runtime close: %w", rt.Name(), inspectErr)
		}
		if !fresh.Ready() {
			return fresh, fmt.Errorf("retirement blocked after runtime state changed: %s", strings.Join(fresh.Blockers, "; "))
		}
		if len(fresh.Sessions) == 0 {
			inspection.Sessions = nil
			inspection.ClosedSessions = len(closed)
			return inspection, nil
		}
		if time.Now().After(deadline) {
			return inspection, fmt.Errorf("timed out waiting for %d %s session(s) to release %s",
				len(fresh.Sessions), rt.Name(), inspection.Target)
		}
		for _, session := range fresh.Sessions {
			handle := session.Runtime.Handle
			if closed[handle] {
				continue
			}
			if err := rt.Close(ctx, handle); err != nil {
				return inspection, fmt.Errorf("close %s runtime %s: %w", rt.Name(), handle, err)
			}
			closed[handle] = true
			break // re-list before authorizing the next destructive close.
		}
		select {
		case <-ctx.Done():
			return inspection, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func coveringAgentStatuses(panes []runtime.Pane, fallback string) []string {
	set := make(map[string]bool)
	for _, pane := range panes {
		if pane.Agent != "" || pane.AgentSession != "" || pane.AgentStatus != "" {
			set[strings.ToLower(pane.AgentStatus)] = true
		}
	}
	if len(set) == 0 {
		set[strings.ToLower(fallback)] = true
	}
	out := make([]string, 0, len(set))
	for status := range set {
		out = append(out, status)
	}
	sort.Strings(out)
	return out
}

func appendAgentStatusBlocker(result *Inspection, handle, rawStatus string, closeUnknown bool) {
	status := strings.ToLower(strings.TrimSpace(rawStatus))
	switch status {
	case "idle", "done":
		// Eligible after all structural checks pass.
	case "working", "running", "busy", "blocked", "waiting":
		result.Blockers = append(result.Blockers,
			fmt.Sprintf("runtime %s has agent status %s", handle, status))
	case "", "unknown":
		if !closeUnknown {
			result.Blockers = append(result.Blockers,
				fmt.Sprintf("runtime %s has unknown agent status; pass --close-unknown from outside it", handle))
		}
	default:
		result.Blockers = append(result.Blockers,
			fmt.Sprintf("runtime %s has unrecognized agent status %s", handle, status))
	}
}

func dedupe(inspection Inspection) Inspection {
	seen := make(map[string]bool)
	out := inspection.Blockers[:0]
	for _, blocker := range inspection.Blockers {
		if !seen[blocker] {
			seen[blocker] = true
			out = append(out, blocker)
		}
	}
	inspection.Blockers = out
	return inspection
}
