package runtime

import (
	"context"
	"errors"
	"strings"
)

// WithSession creates a scoped adapter without changing the original local
// adapter. Every native command explicitly selects this server session and
// excludes inherited socket and client/pane routing hints.
func (h *Herdr) WithSession(session string) (*Herdr, error) {
	if len(session) == 0 || len(session) > 64 || session == "." || session == ".." {
		return nil, errors.New("invalid explicit Herdr session")
	}
	for _, c := range session {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return nil, errors.New("invalid explicit Herdr session")
		}
	}
	copy := *h
	copy.session = session
	return &copy, nil
}

// CheckSession only reads native inventories; it never starts a server,
// creates workspaces, changes client focus, or attaches a client.
func (h *Herdr) CheckSession(ctx context.Context) error {
	if h.session == "" {
		return errors.New("session check requires an explicit Herdr session")
	}
	var workspaces struct {
		Workspaces *[]herdrWorkspace `json:"workspaces"`
	}
	if err := h.call(ctx, &workspaces, "workspace", "list"); err != nil {
		return err
	}
	if workspaces.Workspaces == nil {
		return errors.New("Herdr session omitted workspace inventory")
	}
	var panes struct {
		Panes *[]herdrPane `json:"panes"`
	}
	if err := h.call(ctx, &panes, "pane", "list"); err != nil {
		return err
	}
	if panes.Panes == nil {
		return errors.New("Herdr session omitted pane inventory")
	}
	return nil
}

// VerifyWorkspace proves that the exact returned handle still covers the
// selected checkout in this session. It never repairs or removes a workspace
// when the observation is incomplete or no longer matches.
func (h *Herdr) VerifyWorkspace(ctx context.Context, handle, path string) error {
	if h.session == "" || handle == "" || path == "" {
		return errors.New("workspace verification requires explicit session, handle and checkout")
	}
	rows, err := h.List(ctx)
	if err != nil {
		return err
	}
	matches, covers := 0, false
	for _, row := range rows {
		if row.Handle == handle {
			matches++
			covers = row.Covers(path)
		}
	}
	if matches != 1 || !covers {
		return errors.New("returned Herdr workspace does not uniquely cover the selected checkout")
	}
	return nil
}

// HerdrSessionEnvironment isolates an explicitly selected server session from
// inherited socket and pane routing hints. It retains HERDR_ENV's nesting
// guard and HERDR_CONFIG_PATH's native configuration authority.
func HerdrSessionEnvironment(environment []string) []string {
	out := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "HERDR_SESSION", "HERDR_SOCKET_PATH", "HERDR_CLIENT_SOCKET_PATH", "HERDR_WORKSPACE_ID", "HERDR_TAB_ID", "HERDR_PANE_ID", "HERDR_PANE_RUNTIME_ID":
			continue
		}
		if strings.HasPrefix(key, "HERDR_ACTIVE_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func scopedHerdrEnvironment(environment []string) []string {
	return HerdrSessionEnvironment(environment)
}
