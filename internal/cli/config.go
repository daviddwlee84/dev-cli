package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show, edit, initialise and locate dev's configuration",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Print the effective configuration",
			Long:  "Print the configuration actually in effect: the built-in defaults with any config.toml overlaid.",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				src := app.Cfg.Source
				if src == "" {
					src = "built-in defaults (no config.toml)"
				}
				fmt.Fprintf(app.Err, "# source: %s\n", config.Contract(src))
				return toml.NewEncoder(app.Out).Encode(app.Cfg)
			},
		},
		&cobra.Command{
			Use:   "path",
			Short: "Print the config file path",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Fprintln(app.Out, config.ConfigFile())
				return nil
			},
		},
		newConfigInitCmd(app),
		newConfigEditCmd(app),
	)
	return cmd
}

func newConfigInitCmd(app *App) *cobra.Command {
	var (
		force  bool
		stdout bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter config.toml, detecting this machine's layout",
		Long: `Generate a commented config.toml describing the paths that actually exist on
this machine.

Where repositories live differs from person to person — ~/Documents/Program,
~/src, ~/code, a ghq root — so the generated file is built from what is found
rather than from a fixed set of defaults. A config pointing at directories that
do not exist is worse than none: dev would discover nothing and give no clue
why.

Every value it writes is still a default you can change; nothing is inferred
that you cannot override.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			layout := config.DetectLayout()
			body := renderStarterConfig(layout.Fallbacks())

			if stdout {
				fmt.Fprint(app.Out, body)
				return nil
			}

			path := app.configPath
			if path == "" {
				path = config.ConfigFile()
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite, or --stdout to preview)",
					config.Contract(path))
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return err
			}

			fmt.Fprintf(app.Out, "wrote %s\n\n", config.Contract(path))
			reportDetection(app, layout)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&force, "force", "f", false, "overwrite an existing config")
	f.BoolVar(&stdout, "stdout", false, "print the generated config instead of writing it")
	return cmd
}

// reportDetection explains what was found, so the user can see whether the
// generated config actually matches how they work.
func reportDetection(app *App, l config.Layout) {
	if len(l.Found) == 0 {
		fmt.Fprintln(app.Out, "No repository directories were recognised, so the defaults were used.")
		fmt.Fprintln(app.Out, "Edit paths.scan_roots to point at where you keep projects.")
		return
	}
	fmt.Fprintln(app.Out, "Detected:")
	t := app.newTable("ROOT", "REPOS", "ROLE")
	for _, r := range l.ScanRoots {
		role := "scan root"
		switch r {
		case l.ProjectRoot:
			role = "scan root, new projects land here"
		case l.TriesRoot:
			role = "scan root, experiments"
		}
		t.Add(r, fmt.Sprintf("%d", l.Found[r]), role)
	}
	t.Render(app.Out)
	if len(l.RepoPaths) > 0 {
		fmt.Fprintln(app.Out, "\nExact repositories:")
		for _, path := range l.RepoPaths {
			fmt.Fprintf(app.Out, "  %s\n", path)
		}
	}
	fmt.Fprintf(app.Out, "\nWorktrees will go to %s — change paths.worktree_root if that volume is wrong.\n",
		l.WorktreeRoot)
}

// renderStarterConfig fills the annotated template with detected paths.
func renderStarterConfig(l config.Layout) string {
	roots := make([]string, len(l.ScanRoots))
	for i, r := range l.ScanRoots {
		roots[i] = strconv.Quote(r)
	}
	r := strings.NewReplacer(
		"@@SCAN_ROOTS@@", strings.Join(roots, ", "),
		"@@REPO_PATHS@@", quotedStrings(l.RepoPaths),
		"@@TRIES_ROOT@@", l.TriesRoot,
		"@@PROJECT_ROOT@@", l.ProjectRoot,
		"@@WORKTREE_ROOT@@", l.WorktreeRoot,
	)
	return r.Replace(starterConfig)
}

func quotedStrings(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = strconv.Quote(value)
	}
	return strings.Join(quoted, ", ")
}

const starterConfig = `# dev configuration.
#
# The paths below were detected on this machine by "dev config init"; everything
# else is dev's built-in default. Delete anything you do not want to override.

[paths]
# Where dev looks for repositories. Each root is scanned to a depth of 3, so
# both <root>/<Repo> and <root>/<Category>/<Repo> are found.
scan_roots = [@@SCAN_ROOTS@@]

# Exact repositories which do not sit below a useful scan root. Exact paths
# are considered first and may point through a symlink navigation alias.
repo_paths = [@@REPO_PATHS@@]

# Where "dev try" creates dated experiment directories.
tries_root = "@@TRIES_ROOT@@"

# Where "dev repo clone|new" and "dev graduate" place real projects.
project_root = "@@PROJECT_ROOT@@"

# Where linked worktrees live. Keep this OUTSIDE any repository: a checkout
# nested inside another checkout makes every indexer, file watcher and ripgrep
# run see a second copy of the tree.
#
# Put it wherever suits the machine — a faster volume, a larger disk:
#   worktree_root = "/mnt/fast/worktrees"
worktree_root = "@@WORKTREE_ROOT@@"

# The full path template. Variables: worktree_root, repo, repo_path, branch,
# category, host, date. Filters: |slug (filesystem-safe), |lower, |base.
#
#   "{{worktree_root}}/{{repo}}/{{branch|slug}}"        ~/Worktrees/api/feat-auth
#   "{{worktree_root}}/{{repo|lower}}--{{branch|slug}}" flat, one directory deep
worktree_path = "{{worktree_root}}/{{repo}}/{{branch|slug}}"

# Task registry, stats database and caches. Point this at a private git repo to
# carry your "what am I working on" state between machines.
# state_dir = "~/.local/share/dev"

[runtime]
# auto prefers herdr, then tmux, then zellij, then none. "none" makes dev print a cd
# directive for the shell wrapper instead of opening a session.
backend = "auto"

# Namespace for the workspace metadata dev reports (herdr only).
metadata_source = "dev"

[worktree]
# Gitignored files to carry into a new worktree. Only files that are BOTH
# matched here AND gitignored are copied: a tracked file is already in the new
# checkout on the correct branch, so listing it would overwrite it.
include = [".env", ".env.local"]

# Directories to symlink instead of copying. Opt-in and empty by default:
# sharing node_modules across checkouts breaks native builds often enough that
# it must never happen by accident.
link = []

# "auto" detects from lockfiles (uv.lock -> uv sync, package-lock.json ->
# npm ci, go.mod -> go mod download, ...). Or give an explicit list:
#   post_create = ["uv sync", "pre-commit install"]
post_create = "auto"

# How a new worktree gets its installed dependencies:
#   reinstall  run the install command (always correct, can be slow)
#   copy       duplicate the directory from the source checkout (fast)
#   link       share one directory between checkouts (fastest, and risky)
#   skip       leave the worktree without dependencies
#
# dev narrows an unsound choice back to reinstall and says why — copying a
# virtualenv cannot work, because it bakes its own absolute path into
# pyvenv.cfg and bin/activate.
strategy = "reinstall"

# Per project type. See what applies to a given repo with "dev wt plan".
# [worktree.strategies]
# node = "copy"       # node_modules copies soundly and reinstalling is slow

# Cap on a single post-create command.
provision_timeout = "10m"

[forge]
# The REMOTE dashboard tab queries configured forge CLIs lazily. A short-lived
# cache makes later switches instant; press r in that tab to refresh explicitly.
remote_limit = 100
cache_ttl = "15m"

# Azure DevOps inventory is opt-in because az repos list requires both an
# organization and a team project. Repeat this table for every project wanted.
# [[forge.azure_devops]]
# organization = "https://dev.azure.com/acme"
# project = "Platform"

[bootstrap]
# Recursive scan policy. Zero max_depth means unlimited; the default reaches
# flat, Category/Repo and ghq host/owner/Repo layouts.
max_depth = 8
follow_symlinks = true

# Optional non-destructive symlink catalog. Leave empty until wanted; once set,
# a plain "dev bootstrap" includes its plan and "dev bootstrap --apply" syncs it.
# Put this path first in paths.scan_roots if the catalog should be the UI.
index_root = ""
layout = "flat"              # flat | preserve
relative_links = false

[tui]

[tui.repos]
# Exact columns and order for the local repository view:
# repo | branch | git | remote | size | live | latest | worktrees | tasks | notes | category | path
# "notes" is opt-in to preserve width; selected detail shows it regardless.
columns = ["repo", "branch", "git", "size", "live", "latest", "worktrees", "tasks"]
# activity puts HOT/live/dirty repos first. Other values: latest, name, git, size, tasks.
sort = "activity"
reverse = false

# External programs the dashboard hands the terminal to, each on its own key.
# They run through your shell in the selected row's checkout, so arguments,
# environment variables and your own scripts all behave as typed.
#
# Listed in full rather than left implicit: what is bound should be something
# you can read, not something you discover by pressing keys. Replace freely —
# a configured list replaces these entirely. See "dev tui tools".
#
# Keys are case-sensitive, and cannot take one the dashboard already uses
# (j k g G h l tab / enter o p c s d a r q e H O R 0 1 2 3 ?); dev reports a clash on load.

[[tui.tools]]
key  = "L"
name = "lazygit"
run  = "lazygit"

[[tui.tools]]
key  = "Y"
name = "yazi"
run  = "yazi"

[[tui.tools]]
key  = "E"
name = "editor"
run  = "$EDITOR ."

[[tui.tools]]
key  = "S"
name = "shell"
run  = "$SHELL"

# Add your own — an editor, a script, an alias you already reach for:
#
# [[tui.tools]]
# key  = "V"
# name = "nvim"
# run  = "nvim ."
#
# [[tui.tools]]
# key  = "B"
# name = "vibe"
# run  = "vibe"
# interactive = true   # load + evaluate aliases/functions after the rc file
#
# [[tui.tools]]
# key  = "P"
# name = "plans here"
# run  = "claude-plans-here"
# interactive = true

[stats]
# Record activity locally for "dev stats --heatmap".
sampler = true

# Also import durations from WakaTime, which covers editor time dev cannot see.
wakatime = false
wakatime_config = "~/.wakatime.cfg"
`
