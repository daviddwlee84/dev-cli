package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/skill"
)

func TestStaticDocumentsSkipApplicationStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	badConfig := filepath.Join(home, "bad.toml")
	if err := os.WriteFile(badConfig, []byte("not = [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundled, err := skill.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--skill"}, bundled},
		{[]string{"--skill=true"}, bundled},
		{[]string{"skill", "print"}, bundled},
		{[]string{"help"}, workflowTLDR},
		{[]string{"help", "worktrees"}, "# Worktrees"},
		{[]string{"help", "wt"}, "# Worktrees"},
		{[]string{"--help"}, "Usage:"},
		{[]string{"start", "--help"}, "Usage:"},
		{[]string{"--version"}, "dev version"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := &App{Out: &out, Err: &errOut, interactiveCheck: func() bool { return true }}
			cleanups := 0
			root := newRootCommandWithCleanup(app, func() { cleanups++ })
			root.SetArgs(append([]string{"--config", badConfig, "--color=never"}, tc.args...))
			if err := root.Execute(); err != nil {
				t.Fatalf("static document depends on invalid application config: %v", err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing static output %q: %q", tc.want, out.String())
			}
			if tc.want == bundled && out.String() != bundled {
				t.Fatal("skill output must equal the installable SKILL.md exactly")
			}
			if cleanups != 0 {
				t.Fatal("static document ran startup filesystem cleanup")
			}
			if app.Tasks != nil || app.Catalog != nil || app.Notes != nil || app.Sizes != nil || app.deferredReleaseRefresh {
				t.Fatal("static document initialized application services or scheduled an update check")
			}
			if tc.args[0] != "help" || len(tc.args) != 1 {
				if errOut.Len() != 0 {
					t.Fatalf("unexpected static stderr: %s", &errOut)
				}
			} else if errOut.String() != "\nRead one with: dev help <topic>\n" {
				t.Fatalf("help index stderr contract changed: %s", &errOut)
			}
		})
	}
	for _, name := range []string{"config", "data", "cache"} {
		if _, err := os.Stat(filepath.Join(home, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("static output touched %s: %v", name, err)
		}
	}
}

func TestStaticDocumentsKeepArgumentAndColorValidation(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--skill", "unexpected"}, "unknown command"},
		{[]string{"skill", "print", "unexpected"}, "unknown command"},
		{[]string{"help", "worktrees", "unexpected"}, "accepts at most 1 arg"},
		{[]string{"help", "no-such-topic"}, "no help topic"},
		{[]string{"--skill", "--color=rainbow"}, "color"},
		{[]string{"skill", "print", "--color=rainbow"}, "color"},
		{[]string{"help", "--color=rainbow"}, "color"},
		{[]string{"--skill", "--unknown-static-flag"}, "unknown flag"},
		{[]string{"skill", "print", "--unknown-static-flag"}, "unknown flag"},
		{[]string{"help", "--unknown-static-flag"}, "unknown flag"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := &App{Out: &out, Err: &errOut}
			root := newRootCommandWithCleanup(app, func() { t.Error("invalid static request ran startup cleanup") })
			root.SetArgs(tc.args)
			if err := root.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if out.Len() != 0 {
				t.Fatalf("invalid request printed content: %q", out.String())
			}
		})
	}
}

func TestNonStaticCommandsKeepApplicationStartup(t *testing.T) {
	badConfig := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(badConfig, []byte("not = [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--skill=false"},
		{"status"},
		{"skill", "install", "--check"},
		{"version"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := &App{Out: &out, Err: &errOut}
			cleanups := 0
			root := newRootCommandWithCleanup(app, func() { cleanups++ })
			root.SetArgs(append([]string{"--config", badConfig}, args...))
			if err := root.Execute(); err == nil || !strings.Contains(err.Error(), badConfig) {
				t.Fatalf("normal request no longer validates application config: %v", err)
			}
			if cleanups != 1 {
				t.Fatalf("normal startup cleanup count = %d, want 1", cleanups)
			}
		})
	}
}
