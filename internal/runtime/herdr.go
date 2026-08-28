package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Herdr drives the herdr terminal workspace manager over its CLI, which emits
// JSON on stdout for every subcommand dev needs.
//
// Division of responsibility, deliberately: dev creates worktrees with plain
// `git worktree add` at its own configured path and then asks herdr to *open*
// that path. `herdr worktree create` is not used, because it would put the
// path policy in a second place — and that policy has to hold on machines
// where herdr is not installed at all.
type Herdr struct {
	bin string
	// metadataSource namespaces the workspace metadata tokens dev reports.
	metadataSource string
	// runCommand and worktreeSource are test seams for Herdr's JSON protocol
	// and Git parent-workspace source resolution.
	runCommand     func(context.Context, ...string) ([]byte, error)
	worktreeSource func(context.Context, string) string
}

// NewHerdr returns the herdr adapter.
func NewHerdr() *Herdr { return &Herdr{bin: "herdr", metadataSource: "dev"} }

// WithMetadataSource sets the --source id used for workspace metadata tokens.
func (h *Herdr) WithMetadataSource(src string) *Herdr {
	if src != "" {
		h.metadataSource = src
	}
	return h
}

// Name implements Runtime.
func (h *Herdr) Name() string { return "herdr" }

// Available reports whether the binary exists *and* a server is reachable.
// The binary alone is not enough: every dev call goes through the socket API,
// so a stopped server must degrade to the next backend rather than making
// every command fail.
func (h *Herdr) Available() bool {
	if !haveBinary(h.bin) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := h.run(ctx, "workspace", "list")
	return err == nil
}

func (h *Herdr) run(ctx context.Context, args ...string) ([]byte, error) {
	if h.runCommand != nil {
		return h.runCommand(ctx, args...)
	}
	cmd := exec.CommandContext(ctx, h.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("herdr %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// herdrEnvelope is the common shape of every herdr CLI JSON response.
type herdrEnvelope struct {
	ID     string          `json:"id"`
	Error  *herdrError     `json:"error"`
	Result json.RawMessage `json:"result"`
}

type herdrError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// call runs a herdr subcommand and decodes result into out.
func (h *Herdr) call(ctx context.Context, out any, args ...string) error {
	raw, err := h.run(ctx, args...)
	if err != nil {
		return err
	}
	if out == nil && len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var env herdrEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("herdr %s: unexpected output: %w", strings.Join(args, " "), err)
	}
	if env.Error != nil {
		return fmt.Errorf("herdr %s: %s (%s)", strings.Join(args, " "), env.Error.Message, env.Error.Code)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

type herdrWorkspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	AgentStatus string `json:"agent_status"`
	Focused     bool   `json:"focused"`
	Number      int    `json:"number"`
}

type herdrPane struct {
	PaneID        string `json:"pane_id"`
	WorkspaceID   string `json:"workspace_id"`
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
	Agent         string `json:"agent"`
	Name          string `json:"name"`
	AgentStatus   string `json:"agent_status"`
	AgentSession  *struct {
		Agent string `json:"agent"`
		Value string `json:"value"`
	} `json:"agent_session"`
}

type herdrWorktree struct {
	Path            string `json:"path"`
	Branch          string `json:"branch"`
	Label           string `json:"label"`
	OpenWorkspaceID string `json:"open_workspace_id"`
	IsLinked        bool   `json:"is_linked_worktree"`
}

// List implements Runtime. Workspaces carry no working directory of their own,
// so dev joins them against the pane list — panes are what actually have a cwd.
func (h *Herdr) List(ctx context.Context) ([]Session, error) {
	var ws struct {
		Workspaces []herdrWorkspace `json:"workspaces"`
	}
	if err := h.call(ctx, &ws, "workspace", "list"); err != nil {
		return nil, err
	}
	var ps struct {
		Panes []herdrPane `json:"panes"`
	}
	if err := h.call(ctx, &ps, "pane", "list"); err != nil {
		return nil, err
	}

	dirs := map[string]map[string]bool{}
	agents := map[string]map[string]bool{}
	panes := map[string][]Pane{}
	for _, p := range ps.Panes {
		cwd := p.ForegroundCWD
		if cwd == "" {
			cwd = p.CWD
		}
		if dirs[p.WorkspaceID] == nil {
			dirs[p.WorkspaceID] = map[string]bool{}
		}
		for _, dir := range []string{p.CWD, cwd} {
			if dir != "" {
				dirs[p.WorkspaceID][dir] = true
			}
		}
		agentSession := ""
		if p.AgentSession != nil && p.AgentSession.Value != "" {
			agent := p.AgentSession.Agent
			if agent == "" {
				agent = p.Agent
			}
			agentSession = agent + ":" + p.AgentSession.Value
			if agents[p.WorkspaceID] == nil {
				agents[p.WorkspaceID] = map[string]bool{}
			}
			agents[p.WorkspaceID][agentSession] = true
		}
		panes[p.WorkspaceID] = append(panes[p.WorkspaceID], Pane{
			ID: p.PaneID, CWD: cwd, ShellCWD: p.CWD, Agent: p.Agent,
			AgentStatus: p.AgentStatus, AgentSession: agentSession,
		})
	}

	out := make([]Session, 0, len(ws.Workspaces))
	for _, w := range ws.Workspaces {
		out = append(out, Session{
			Handle:        w.WorkspaceID,
			Label:         w.Label,
			Dirs:          keys(dirs[w.WorkspaceID]),
			AgentSessions: keys(agents[w.WorkspaceID]),
			AgentStatus:   w.AgentStatus,
			Panes:         panes[w.WorkspaceID],
			Focused:       w.Focused,
		})
	}
	return out, nil
}

// CurrentPaneID implements CurrentPaneResolver without changing focus. --current
// resolves Herdr's inherited caller context even when a pane move changed its
// public ID after this process started.
func (h *Herdr) CurrentPaneID(ctx context.Context) (string, error) {
	var res struct {
		Pane herdrPane `json:"pane"`
	}
	if err := h.call(ctx, &res, "pane", "current", "--current"); err != nil {
		return "", err
	}
	if res.Pane.PaneID == "" {
		return "", fmt.Errorf("herdr pane current --current: response has no pane id")
	}
	return res.Pane.PaneID, nil
}

// AgentActivities implements AgentActivityLister using Herdr's recognized-agent
// inventory. Every returned row is occupied; lifecycle state is descriptive and
// is deliberately not used to decide whether another agent may share its cwd.
func (h *Herdr) AgentActivities(ctx context.Context) ([]AgentActivity, error) {
	var res struct {
		Agents *[]herdrPane `json:"agents"`
	}
	if err := h.call(ctx, &res, "agent", "list"); err != nil {
		return nil, err
	}
	if res.Agents == nil {
		return nil, fmt.Errorf("herdr agent list: response has no agents array")
	}
	out := make([]AgentActivity, 0, len(*res.Agents))
	for _, p := range *res.Agents {
		cwd := p.ForegroundCWD
		if cwd == "" {
			cwd = p.CWD
		}
		if cwd == "" {
			return nil, fmt.Errorf("herdr agent list: recognized agent in pane %s has no cwd", p.PaneID)
		}
		out = append(out, AgentActivity{
			PaneID: p.PaneID, WorkspaceID: p.WorkspaceID,
			Agent: p.Agent, Name: p.Name, Status: p.AgentStatus, CWD: cwd,
		})
	}
	return out, nil
}

// Open implements Runtime. An already-open directory is reused rather than
// opened twice. It remains detached; explicit navigation calls Activate.
func (h *Herdr) Open(ctx context.Context, dir, label string) (OpenResult, error) {
	existing, err := h.List(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	for _, s := range existing {
		if s.Covers(dir) {
			return OpenResult{Handle: s.Handle, Surface: "workspace", Opened: true}, nil
		}
	}
	args := []string{"workspace", "create", "--cwd", dir, "--no-focus"}
	if label != "" {
		args = append(args, "--label", label)
	}
	var res struct {
		Workspace herdrWorkspace `json:"workspace"`
		RootPane  herdrPane      `json:"root_pane"`
	}
	if err := h.call(ctx, &res, args...); err != nil {
		return OpenResult{}, err
	}
	if res.Workspace.WorkspaceID == "" {
		return OpenResult{}, fmt.Errorf("herdr workspace create: response has no workspace id")
	}
	if res.RootPane.PaneID == "" || res.RootPane.WorkspaceID != res.Workspace.WorkspaceID {
		return OpenResult{}, h.rejectCreatedWorkspace(ctx, res.Workspace.WorkspaceID,
			"herdr workspace create: response has no correlated root pane")
	}
	return OpenResult{
		Handle: res.Workspace.WorkspaceID, Surface: "workspace", Opened: true,
		Created: true, RootPaneID: res.RootPane.PaneID,
	}, nil
}

// Activate focuses a workspace and then mirrors hhere's client behavior:
// inside Herdr the existing client switches immediately; outside Herdr a new
// client attaches after the caller has left any alternate-screen UI.
func (h *Herdr) Activate(ctx context.Context, handle string) error {
	if handle == "" {
		return nil
	}
	if err := h.Focus(ctx, handle); err != nil {
		return err
	}
	if os.Getenv("HERDR_ENV") != "" {
		return nil
	}
	return runInteractive(ctx, h.bin)
}

// Focus selects a workspace without attaching a client. Remote fleet
// navigation uses it before launching a local `herdr --remote` thin client.
func (h *Herdr) Focus(ctx context.Context, handle string) error {
	if handle == "" {
		return nil
	}
	return h.call(ctx, nil, "workspace", "focus", handle)
}

// OpenWorktree implements WorktreeOpener: it registers an already-created git
// worktree as a herdr workspace, which is what makes the checkout appear in
// the sidebar grouped under its parent repo with its own branch and
// ahead/behind row.
func (h *Herdr) OpenWorktree(ctx context.Context, path, label string) (OpenResult, error) {
	args := []string{"worktree", "open"}
	if source := h.parentWorktreeSource(ctx, path); source != "" {
		// Pin the canonical parent checkout instead of relying on the calling
		// pane. This preserves native nested grouping even when dev is invoked
		// from a different Herdr workspace.
		args = append(args, "--cwd", source)
	}
	args = append(args, "--path", path, "--no-focus")
	if label != "" {
		args = append(args, "--label", label)
	}
	var res struct {
		Workspace   herdrWorkspace `json:"workspace"`
		Worktree    herdrWorktree  `json:"worktree"`
		RootPane    herdrPane      `json:"root_pane"`
		AlreadyOpen *bool          `json:"already_open"`
	}
	if err := h.call(ctx, &res, args...); err != nil {
		// Fall back to a plain workspace: the checkout being visible matters
		// more than it being tagged with git provenance. A fallback root pane is
		// withheld so callers cannot mistake it for a first-class worktree target.
		fallback, fallbackErr := h.Open(ctx, path, label)
		fallback.RootPaneID = ""
		fallback.Surface = "workspace"
		return fallback, fallbackErr
	}
	handle := res.Workspace.WorkspaceID
	if handle == "" {
		handle = res.Worktree.OpenWorkspaceID
	}
	if handle == "" {
		return OpenResult{}, fmt.Errorf("herdr worktree open: response has no workspace id")
	}
	if res.AlreadyOpen == nil {
		// Without this protocol field, the handle may belong to a reused
		// workspace. Do not close it or expose its pane as newly created.
		return OpenResult{}, fmt.Errorf("herdr worktree open: response does not say whether the workspace was reused")
	}
	created := !*res.AlreadyOpen
	if created && (res.RootPane.PaneID == "" || res.RootPane.WorkspaceID != handle) {
		return OpenResult{}, h.rejectCreatedWorkspace(ctx, handle,
			"herdr worktree open: new workspace response has no correlated root pane")
	}
	rootPaneID := ""
	if created {
		rootPaneID = res.RootPane.PaneID
	}
	return OpenResult{
		Handle: handle, Surface: "worktree", Opened: true,
		Created: created, RootPaneID: rootPaneID,
	}, nil
}

func (h *Herdr) rejectCreatedWorkspace(ctx context.Context, handle, reason string) error {
	if err := h.Close(ctx, handle); err != nil {
		return fmt.Errorf("%s; could not close incomplete workspace %s: %w", reason, handle, err)
	}
	return fmt.Errorf("%s; closed incomplete workspace %s", reason, handle)
}

func (h *Herdr) parentWorktreeSource(ctx context.Context, path string) string {
	if h.worktreeSource != nil {
		return h.worktreeSource(ctx, path)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", path, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var parent string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree ") && parent == "":
			parent = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case line == "" && parent != "":
			if info, err := os.Stat(parent); err == nil && info.IsDir() {
				return filepath.Clean(parent)
			}
			return ""
		}
	}
	return ""
}

// Close implements Runtime. This ends herdr's session state only — it never
// removes the checkout, the worktree or the branch, matching herdr's own
// separation of `workspace close` from `worktree remove`.
func (h *Herdr) Close(ctx context.Context, handle string) error {
	if handle == "" {
		return nil
	}
	return h.call(ctx, nil, "workspace", "close", handle)
}

// Annotate implements Runtime by reporting display-only workspace metadata.
//
// Note for users: a token only renders if the sidebar row layout in
// ~/.config/herdr/config.toml names it, e.g. rows = [[..., "$stage"], ["$next"]].
// Setting a token with no matching layout entry succeeds silently and shows
// nothing.
func (h *Herdr) Annotate(ctx context.Context, handle string, kv map[string]string) error {
	if handle == "" || len(kv) == 0 {
		return nil
	}
	args := []string{"workspace", "report-metadata", handle, "--source", h.metadataSource}
	for k, v := range kv {
		if v == "" {
			args = append(args, "--clear-token", k)
			continue
		}
		args = append(args, "--token", k+"="+v)
	}
	return h.call(ctx, nil, args...)
}

func keys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
