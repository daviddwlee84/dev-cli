package runtime

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// HostKind names the terminal environment dev itself is running in. It is an
// observation of inherited caller context, independent of which runtime
// backend is configured.
type HostKind string

const (
	HostHerdr     HostKind = "herdr"
	HostHerdrCore HostKind = "herdr-core"
	HostTmux      HostKind = "tmux"
	HostZellij    HostKind = "zellij"
	HostPlain     HostKind = "plain"
)

// GUI hosts of a herdr-core terminal. Herdr Desktop and Sheprd are different
// products; GUIUnknown never becomes a guess.
const (
	GUIHerdrDesktop = "herdr-desktop"
	GUISheprd       = "sheprd"
	GUIUnknown      = "unknown"
)

// HostContext is what DetectHost observed.
type HostContext struct {
	Kind HostKind
	// GUI is set only for herdr-core: which window shows this terminal.
	GUI string
	// TerminalID is HC_TERMINAL_ID inside herdr-core.
	TerminalID string
	// Evidence lists the environment markers seen, innermost first.
	Evidence []string
}

// Describe renders the context for doctor and the dashboard.
func (c HostContext) Describe() string {
	switch c.Kind {
	case HostHerdrCore:
		gui := "GUI unknown"
		switch c.GUI {
		case GUIHerdrDesktop:
			gui = "Herdr Desktop"
		case GUISheprd:
			gui = "Sheprd"
		}
		if c.TerminalID != "" {
			return "herdr-core (" + gui + ", " + c.TerminalID + ")"
		}
		return "herdr-core (" + gui + ")"
	case "":
		return string(HostPlain)
	}
	return string(c.Kind)
}

// hostProbe carries the seams DetectHost needs.
type hostProbe struct {
	getenv func(string) string
	core   *HerdrCore
	stat   func(string) error
}

// DetectHost classifies the caller's terminal environment. Markers are read
// innermost first: a tmux started inside an hc terminal is a tmux host. The
// only process it may run is one bounded Herdr Desktop layout read to learn
// whether that window shows this terminal.
func DetectHost(ctx context.Context) HostContext {
	return detectHost(ctx, hostProbe{getenv: os.Getenv, core: NewHerdrCore(), stat: func(p string) error {
		_, err := os.Stat(p)
		return err
	}})
}

func detectHost(ctx context.Context, p hostProbe) HostContext {
	var c HostContext
	markers := []struct {
		kind HostKind
		env  string
	}{
		{HostTmux, "TMUX"}, {HostZellij, "ZELLIJ"}, {HostHerdr, "HERDR_ENV"}, {HostHerdrCore, "HC_TERMINAL_ID"},
	}
	for _, m := range markers {
		if p.getenv(m.env) == "" {
			continue
		}
		c.Evidence = append(c.Evidence, m.env)
		if c.Kind == "" {
			c.Kind = m.kind
		}
	}
	if c.Kind == "" {
		c.Kind = HostPlain
		return c
	}
	if c.Kind != HostHerdrCore {
		return c
	}
	c.TerminalID = p.getenv("HC_TERMINAL_ID")
	c.GUI = GUIUnknown
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if spaces, err := p.core.spaces(ctx); err == nil {
		for _, s := range spaces {
			for _, address := range s.terminals() {
				if address == c.TerminalID {
					c.GUI = GUIHerdrDesktop
					c.Evidence = append(c.Evidence, "herdr-app ls shows "+address)
					return c
				}
			}
		}
	}
	// Sheprd's control socket is only stat-ed. Its presence with this terminal
	// absent from Herdr Desktop is recorded as evidence, not a GUI claim.
	if home, err := os.UserHomeDir(); err == nil && p.stat != nil {
		if p.stat(filepath.Join(home, ".local", "state", "herdr", "gpui", "control.sock")) == nil {
			c.Evidence = append(c.Evidence, "sheprd control socket present")
		}
	}
	return c
}
