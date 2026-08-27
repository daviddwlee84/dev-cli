package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"feat/auth/oauth-refresh", "feat-auth-oauth-refresh"},
		{"main", "main"},
		{"fix/GX-123_race", "fix-GX-123_race"},
		{"feat//double", "feat-double"},
		{"/leading/trailing/", "leading-trailing"},
		{"...", "unnamed"},
		{"", "unnamed"},
		{"release/v1.2.0", "release-v1.2.0"},
	} {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRender(t *testing.T) {
	vars := Vars{"worktree_root": "/mnt/wt", "repo": "MyRepo", "branch": "feat/x"}

	for _, tc := range []struct{ tmpl, want string }{
		{"{{worktree_root}}/{{repo}}/{{branch|slug}}", "/mnt/wt/MyRepo/feat-x"},
		{"{{worktree_root}}/{{repo|lower}}-{{branch|slug}}", "/mnt/wt/myrepo-feat-x"},
		{"{{ worktree_root }}/{{ branch | slug }}", "/mnt/wt/feat-x"},
		{"{{branch|base}}", "x"},
		{"no-vars", "no-vars"},
	} {
		got, err := Render(tc.tmpl, vars)
		if err != nil {
			t.Fatalf("Render(%q): %v", tc.tmpl, err)
		}
		if got != tc.want {
			t.Errorf("Render(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

func TestRenderRejectsUnknown(t *testing.T) {
	vars := Vars{"repo": "r"}
	if _, err := Render("{{rep}}", vars); err == nil {
		t.Error("unknown variable should be an error, not a literal passthrough")
	} else if !strings.Contains(err.Error(), "known:") {
		t.Errorf("error should list known variables, got %v", err)
	}
	if _, err := Render("{{repo|shout}}", vars); err == nil {
		t.Error("unknown filter should be an error")
	}
}

func TestExpand(t *testing.T) {
	h, _ := os.UserHomeDir()
	if got := Expand("~/Worktrees"); got != filepath.Join(h, "Worktrees") {
		t.Errorf("Expand(~/Worktrees) = %q", got)
	}
	if got := Expand("~"); got != h {
		t.Errorf("Expand(~) = %q, want %q", got, h)
	}
	// A path that merely starts with ~ but is not the home shorthand.
	if got := Expand("/tmp/~notme"); got != "/tmp/~notme" {
		t.Errorf("Expand(/tmp/~notme) = %q", got)
	}
	t.Setenv("DEV_TEST_ROOT", "/mnt/fast")
	if got := Expand("$DEV_TEST_ROOT/wt"); got != "/mnt/fast/wt" {
		t.Errorf("Expand($DEV_TEST_ROOT/wt) = %q", got)
	}
	if Contract(filepath.Join(h, "x")) != "~/x" {
		t.Errorf("Contract did not collapse home")
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("a missing config must not be an error: %v", err)
	}
	if cfg.Runtime.Backend != "auto" {
		t.Errorf("backend = %q, want auto", cfg.Runtime.Backend)
	}
	if !cfg.Worktree.PostCreate.Auto {
		t.Error("post_create should default to auto")
	}
}

func TestLoadOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
[paths]
worktree_root = "/mnt/fast/wt"
worktree_path = "{{worktree_root}}/{{repo|lower}}/{{branch|slug}}"

[runtime]
backend = "tmux"

[worktree]
include = [".env", "config/secrets.json"]
post_create = ["uv sync"]
provision_timeout = "90s"
`), 0o644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Backend != "tmux" {
		t.Errorf("backend = %q", cfg.Runtime.Backend)
	}
	if cfg.Worktree.PostCreate.Auto || len(cfg.Worktree.PostCreate.Commands) != 1 {
		t.Errorf("post_create = %+v", cfg.Worktree.PostCreate)
	}
	if cfg.Worktree.ProvisionTimeout.Seconds() != 90 {
		t.Errorf("provision_timeout = %v", cfg.Worktree.ProvisionTimeout)
	}
	// Defaults for untouched sections survive the overlay.
	if cfg.Paths.TriesRoot == "" {
		t.Error("tries_root should have kept its default")
	}
	got, err := cfg.WorktreePathFor("MyRepo", "/src/MyRepo", "feat/auth", "Quant")
	if err != nil {
		t.Fatalf("WorktreePathFor: %v", err)
	}
	if got != "/mnt/fast/wt/myrepo/feat-auth" {
		t.Errorf("WorktreePathFor = %q", got)
	}
}

func TestValidateRejectsBadBackendAndTemplate(t *testing.T) {
	c := Default()
	c.Runtime.Backend = "screen"
	if err := c.Validate(); err == nil {
		t.Error("unknown backend should fail validation")
	}
	c = Default()
	c.Paths.WorktreePath = "{{worktre_root}}/x"
	if err := c.Validate(); err == nil {
		t.Error("typo'd template variable should fail validation")
	}
}
