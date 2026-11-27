package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

// Open target IDs beyond tui.OpenTargetRuntime and tui.OpenTargetCD. They are
// also the values accepted by tui.open.default.
const (
	openTargetHerdrDesktop = "herdr-desktop"
	openTargetHerdrCore    = "herdr-core"
	openTargetHerdr        = "herdr"
	openTargetSheprd       = "sheprd"
	openTargetTmux         = "tmux"
	openTargetZellij       = "zellij"
)

// openTargetProbe observes which Open targets this host offers. Every probe is
// bounded and read-only: binary lookup, server/window status, never a launch.
type openTargetProbe struct {
	getenv    func(string) string
	lookPath  func(string) (string, error)
	herdr     func() bool
	herdrCore func() bool
	desktop   func(context.Context) bool
}

func defaultOpenTargetProbe() openTargetProbe {
	core := runtime.NewHerdrCore()
	return openTargetProbe{
		getenv: os.Getenv, lookPath: exec.LookPath,
		herdr: runtime.NewHerdr().Available, herdrCore: core.Available, desktop: core.DesktopAvailable,
	}
}

// computeOpenTargets lists where Enter can open a checkout. A target backed by
// the configured runtime replaces the generic runtime row instead of
// repeating it. Mode "default" collapses the list to the one configured
// target, which opens without asking.
func computeOpenTargets(ctx context.Context, active string, open openSettings, p openTargetProbe) []tui.OpenTarget {
	type candidate struct {
		target  tui.OpenTarget
		backend string
	}
	var candidates []candidate
	if p.herdrCore != nil && p.herdrCore() {
		if p.desktop != nil && p.desktop(ctx) {
			candidates = append(candidates, candidate{tui.OpenTarget{ID: openTargetHerdrDesktop, Label: "Herdr Desktop space"}, "herdr-core"})
		} else {
			candidates = append(candidates, candidate{tui.OpenTarget{ID: openTargetHerdrCore, Label: "herdr-core terminal", Detail: "no Herdr Desktop window"}, "herdr-core"})
		}
	}
	if p.herdr != nil && p.herdr() {
		detail := ""
		if p.getenv("HERDR_ENV") == "" {
			detail = "attaches a Herdr client here"
		}
		candidates = append(candidates, candidate{tui.OpenTarget{ID: openTargetHerdr, Label: "Herdr workspace", Detail: detail}, "herdr"})
	}
	// tmux and zellij are listed only from inside them (or when configured):
	// being installed is not a sign someone wants a session opened there.
	for _, mux := range []struct{ id, env, label string }{{openTargetTmux, "TMUX", "tmux session"}, {openTargetZellij, "ZELLIJ", "zellij session"}} {
		if p.getenv(mux.env) != "" || active == mux.id {
			candidates = append(candidates, candidate{tui.OpenTarget{ID: mux.id, Label: mux.label}, mux.id})
		}
	}
	if _, err := p.lookPath("sheprd"); err == nil {
		candidates = append(candidates, candidate{tui.OpenTarget{ID: openTargetSheprd, Label: "Sheprd workspace", Detail: "sheprd open"}, ""})
	}

	var out []tui.OpenTarget
	runtimeIndex := -1
	for _, c := range candidates {
		if c.backend != "" && c.backend == active && runtimeIndex < 0 {
			c.target.Detail = strings.TrimSpace("configured runtime " + c.target.Detail)
			runtimeIndex = len(out)
		}
		out = append(out, c.target)
	}
	if runtimeIndex < 0 && active != "none" {
		out = append([]tui.OpenTarget{{ID: tui.OpenTargetRuntime, Label: "configured runtime (" + active + ")"}}, out...)
		runtimeIndex = 0
	}
	// With no runtime, the configured behavior already is a cd.
	out = append(out, tui.OpenTarget{ID: tui.OpenTargetCD, Label: "cd here", Detail: "shell wrapper"})
	if runtimeIndex < 0 {
		runtimeIndex = len(out) - 1
	}

	chosen := runtimeIndex
	if open.Default != "" && open.Default != tui.OpenTargetRuntime {
		for i, t := range out {
			if t.ID == open.Default || (open.Default == openTargetHerdrCore && t.ID == openTargetHerdrDesktop) {
				chosen = i
				break
			}
		}
	}
	out[chosen].Default = true
	if open.Mode == "default" {
		return []tui.OpenTarget{out[chosen]}
	}
	return out
}

type openSettings struct{ Mode, Default string }

// openTargetCache probes in the background when the dashboard starts, so the
// first Enter normally finds the answer ready.
type openTargetCache struct {
	once    sync.Once
	ready   chan struct{}
	targets []tui.OpenTarget
	compute func() []tui.OpenTarget
}

func newOpenTargetCache(compute func() []tui.OpenTarget) *openTargetCache {
	c := &openTargetCache{ready: make(chan struct{}), compute: compute}
	c.start()
	return c
}

func (c *openTargetCache) start() {
	c.once.Do(func() {
		go func() {
			c.targets = c.compute()
			close(c.ready)
		}()
	})
}

// Get waits briefly for the probe. A probe that has not finished yields no
// picker, and Enter falls back to the configured runtime.
func (c *openTargetCache) Get() []tui.OpenTarget {
	select {
	case <-c.ready:
		return c.targets
	case <-time.After(3 * time.Second):
		return nil
	}
}

// openTargetRuntime resolves the backend a target opens in. ok is false for
// targets that are not runtimes (cd, Sheprd).
func openTargetRuntime(app *App, target string) (runtime.Runtime, bool) {
	switch target {
	case openTargetHerdrDesktop, openTargetHerdrCore:
		return app.runtimeNamed("herdr-core"), true
	case openTargetHerdr, openTargetTmux, openTargetZellij:
		return app.runtimeNamed(target), true
	}
	return nil, false
}

// openInSheprd asks a running (or newly started) Sheprd to focus or create
// the folder's workspace, as its Open Project palette command does.
func openInSheprd(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sheprd", "open", dir, "--json")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 3:
			return errors.New("sheprd open: Sheprd is not running")
		case 5:
			return errors.New("sheprd open: no Sheprd window shows this terminal")
		case 6:
			return errors.New("sheprd open: Sheprd is busy with a dialog")
		case 7:
			return errors.New("sheprd open: Sheprd did not reach the local Herdr in time")
		}
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("sheprd open: %s", msg)
}
