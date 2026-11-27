package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func fakeOpenProbe(env map[string]string, herdr, core, desktop, sheprd bool) openTargetProbe {
	return openTargetProbe{
		getenv: func(k string) string { return env[k] },
		lookPath: func(name string) (string, error) {
			if name == "sheprd" && sheprd {
				return "/bin/sheprd", nil
			}
			return "", errors.New("absent")
		},
		herdr:     func() bool { return herdr },
		herdrCore: func() bool { return core },
		desktop:   func(context.Context) bool { return desktop },
	}
}

func targetIDs(targets []tui.OpenTarget) (ids []string, def string) {
	for _, t := range targets {
		ids = append(ids, t.ID)
		if t.Default {
			def = t.ID
		}
	}
	return ids, def
}

func TestComputeOpenTargets(t *testing.T) {
	ask := openSettings{Mode: "ask", Default: "runtime"}
	for _, tc := range []struct {
		name   string
		active string
		open   openSettings
		probe  openTargetProbe
		ids    []string
		def    string
	}{
		{
			name: "herdr desktop host merges the runtime row", active: "herdr-core", open: ask,
			probe: fakeOpenProbe(map[string]string{"HC_TERMINAL_ID": "t2"}, true, true, true, true),
			ids:   []string{"herdr-desktop", "herdr", "sheprd", "cd"}, def: "herdr-desktop",
		},
		{
			name: "hc without a window offers a terminal", active: "herdr", open: ask,
			probe: fakeOpenProbe(map[string]string{}, true, true, false, false),
			ids:   []string{"herdr-core", "herdr", "cd"}, def: "herdr",
		},
		{
			name: "no runtime collapses to cd", active: "none", open: ask,
			probe: fakeOpenProbe(map[string]string{}, false, false, false, false),
			ids:   []string{"cd"}, def: "cd",
		},
		{
			name: "tmux is listed only from inside tmux", active: "herdr", open: ask,
			probe: fakeOpenProbe(map[string]string{"TMUX": "/tmp/t"}, true, false, false, false),
			ids:   []string{"herdr", "tmux", "cd"}, def: "herdr",
		},
		{
			name: "configured default preselects", active: "herdr-core", open: openSettings{Mode: "ask", Default: "cd"},
			probe: fakeOpenProbe(map[string]string{}, true, true, true, false),
			ids:   []string{"herdr-desktop", "herdr", "cd"}, def: "cd",
		},
		{
			name: "default mode never asks", active: "herdr-core", open: openSettings{Mode: "default", Default: "herdr"},
			probe: fakeOpenProbe(map[string]string{}, true, true, true, true),
			ids:   []string{"herdr"}, def: "herdr",
		},
		{
			name: "runtime not offered elsewhere keeps a generic row", active: "zellij", open: ask,
			probe: fakeOpenProbe(map[string]string{}, false, false, false, true),
			ids:   []string{"zellij", "sheprd", "cd"}, def: "zellij",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids, def := targetIDs(computeOpenTargets(context.Background(), tc.active, tc.open, tc.probe))
			if len(ids) != len(tc.ids) {
				t.Fatalf("ids = %v, want %v", ids, tc.ids)
			}
			for i := range ids {
				if ids[i] != tc.ids[i] {
					t.Fatalf("ids = %v, want %v", ids, tc.ids)
				}
			}
			if def != tc.def {
				t.Fatalf("default = %q, want %q", def, tc.def)
			}
		})
	}
}
