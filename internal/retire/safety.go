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
	canonicalTarget, err := pathx.Canonical(target)
	if err != nil {
		return Inspection{}, fmt.Errorf("canonicalize retirement target: %w", err)
	}
	result := Inspection{Target: canonicalTarget}
	cwd := opts.CWD
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return Inspection{}, fmt.Errorf("resolve caller directory: %w", err)
		}
	}
	if inside, compareErr := pathx.Contains(canonicalTarget, cwd); compareErr != nil {
		return Inspection{}, fmt.Errorf("compare caller directory: %w", compareErr)
	} else if inside {
		result.CallerContained = true
		result.Blockers = append(result.Blockers,
			fmt.Sprintf("caller is inside target checkout %s", canonicalTarget))
	}

	if opts.CallerWorkspaceID == "" {
		opts.CallerWorkspaceID = os.Getenv("HERDR_WORKSPACE_ID")
	}
	if opts.CallerPaneID == "" {
		opts.CallerPaneID = os.Getenv("HERDR_PANE_ID")
		if opts.CallerPaneID == "" {
			opts.CallerPaneID = os.Getenv("TMUX_PANE")
		}
	}
	if rt == nil || rt.Name() == "none" {
		return dedupe(result), nil
	}
	sessions, err := rt.List(ctx)
	if err != nil {
		if !opts.AssumeNoRuntime {
			return Inspection{}, fmt.Errorf("list %s runtime sessions: %w", rt.Name(), err)
		}
		result.RuntimeUnknown = true
		return dedupe(result), nil
	}
	for _, observed := range sessions {
		covering, mixed, err := classifySession(canonicalTarget, observed)
		if err != nil {
			return Inspection{}, err
		}
		if len(covering) == 0 {
			continue
		}
		result.Sessions = append(result.Sessions, Session{Runtime: observed, Panes: covering, Mixed: mixed})
		if observed.Handle == opts.CallerWorkspaceID || panePresent(observed.Panes, opts.CallerPaneID) {
			result.CallerContained = true
			result.Blockers = append(result.Blockers,
				fmt.Sprintf("caller runtime %s contains the target checkout", observed.Handle))
		}
		if len(mixed) > 0 {
			result.Blockers = append(result.Blockers,
				fmt.Sprintf("runtime %s also contains %d pane(s) outside the target", observed.Handle, len(mixed)))
		}
		statuses := coveringAgentStatuses(covering, observed.AgentStatus)
		for _, status := range statuses {
			switch status {
			case "idle", "done":
				// Eligible after all structural checks pass.
			case "working", "running", "busy", "blocked", "waiting":
				result.Blockers = append(result.Blockers,
					fmt.Sprintf("runtime %s has agent status %s", observed.Handle, status))
			case "", "unknown":
				if !opts.CloseUnknown {
					result.Blockers = append(result.Blockers,
						fmt.Sprintf("runtime %s has unknown agent status; pass --close-unknown from outside it", observed.Handle))
				}
			default:
				result.Blockers = append(result.Blockers,
					fmt.Sprintf("runtime %s has unrecognized agent status %s", observed.Handle, status))
			}
		}
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

func classifySession(target string, session runtime.Session) ([]runtime.Pane, []runtime.Pane, error) {
	panes := session.Panes
	if len(panes) == 0 {
		for i, dir := range session.Dirs {
			panes = append(panes, runtime.Pane{ID: fmt.Sprintf("%s:%d", session.Handle, i), CWD: dir})
		}
	}
	var covering, mixed []runtime.Pane
	for _, pane := range panes {
		paths := []string{pane.CWD}
		if pane.ShellCWD != "" && pane.ShellCWD != pane.CWD {
			paths = append(paths, pane.ShellCWD)
		}
		paneCovers, paneOutside, observed := false, false, false
		for _, path := range paths {
			if path == "" {
				continue
			}
			observed = true
			inside, err := pathx.Contains(target, path)
			if err != nil {
				return nil, nil, fmt.Errorf("compare runtime pane %s path: %w", pane.ID, err)
			}
			if inside {
				paneCovers = true
			} else {
				paneOutside = true
			}
		}
		if paneCovers {
			covering = append(covering, pane)
		}
		if paneOutside || !observed {
			mixed = append(mixed, pane)
		}
	}
	return covering, mixed, nil
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

func panePresent(panes []runtime.Pane, id string) bool {
	if id == "" {
		return false
	}
	for _, pane := range panes {
		if pane.ID == id {
			return true
		}
	}
	return false
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
