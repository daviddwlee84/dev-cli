package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
	PaneID       string `json:"pane_id"`
	WorkspaceID  string `json:"workspace_id"`
	CWD          string `json:"cwd"`
	Agent        string `json:"agent"`
	AgentSession *struct {
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
		// Directories are an enrichment; a workspace listing without them is
		// still worth returning.
		ps.Panes = nil
	}

	dirs := map[string]map[string]bool{}
	agents := map[string]map[string]bool{}
	for _, p := range ps.Panes {
		if p.CWD != "" {
			if dirs[p.WorkspaceID] == nil {
				dirs[p.WorkspaceID] = map[string]bool{}
			}
			dirs[p.WorkspaceID][p.CWD] = true
		}
		if p.AgentSession != nil && p.AgentSession.Value != "" {
			if agents[p.WorkspaceID] == nil {
				agents[p.WorkspaceID] = map[string]bool{}
			}
			agents[p.WorkspaceID][p.AgentSession.Agent+":"+p.AgentSession.Value] = true
		}
	}

	out := make([]Session, 0, len(ws.Workspaces))
	for _, w := range ws.Workspaces {
		out = append(out, Session{
			Handle:        w.WorkspaceID,
			Label:         w.Label,
			Dirs:          keys(dirs[w.WorkspaceID]),
			AgentSessions: keys(agents[w.WorkspaceID]),
			AgentStatus:   w.AgentStatus,
			Focused:       w.Focused,
		})
	}
	return out, nil
}

// Open implements Runtime. An already-open directory is focused rather than
// opened twice.
func (h *Herdr) Open(ctx context.Context, dir, label string) (string, error) {
	if existing, err := h.List(ctx); err == nil {
		for _, s := range existing {
			for _, d := range s.Dirs {
				if d == dir {
					_ = h.call(ctx, nil, "workspace", "focus", s.Handle)
					return s.Handle, nil
				}
			}
		}
	}
	args := []string{"workspace", "create", "--cwd", dir, "--no-focus"}
	if label != "" {
		args = append(args, "--label", label)
	}
	var res struct {
		Workspace herdrWorkspace `json:"workspace"`
	}
	if err := h.call(ctx, &res, args...); err != nil {
		return "", err
	}
	return res.Workspace.WorkspaceID, nil
}

// OpenWorktree implements WorktreeOpener: it registers an already-created git
// worktree as a herdr workspace, which is what makes the checkout appear in
// the sidebar grouped under its parent repo with its own branch and
// ahead/behind row.
func (h *Herdr) OpenWorktree(ctx context.Context, path, label string) (string, error) {
	args := []string{"worktree", "open", "--path", path, "--no-focus"}
	if label != "" {
		args = append(args, "--label", label)
	}
	var res struct {
		Workspace herdrWorkspace `json:"workspace"`
		Worktree  herdrWorktree  `json:"worktree"`
	}
	if err := h.call(ctx, &res, args...); err != nil {
		// Fall back to a plain workspace: the checkout being visible matters
		// more than it being tagged with git provenance.
		return h.Open(ctx, path, label)
	}
	if res.Workspace.WorkspaceID != "" {
		return res.Workspace.WorkspaceID, nil
	}
	return res.Worktree.OpenWorkspaceID, nil
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
