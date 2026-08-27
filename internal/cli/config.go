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
		Short: "Show, initialise and locate dev's configuration",
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
	t := NewTable("ROOT", "REPOS", "ROLE")
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
		"@@TRIES_ROOT@@", l.TriesRoot,
		"@@PROJECT_ROOT@@", l.ProjectRoot,
		"@@WORKTREE_ROOT@@", l.WorktreeRoot,
	)
	return r.Replace(starterConfig)
}

const starterConfig = `# dev configuration.
#
# The paths below were detected on this machine by "dev config init"; everything
# else is dev's built-in default. Delete anything you do not want to override.

[paths]
# Where dev looks for repositories. Each root is scanned to a depth of 3, so
# both <root>/<Repo> and <root>/<Category>/<Repo> are found.
scan_roots = [@@SCAN_ROOTS@@]

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
# auto prefers herdr, then tmux, then none. "none" makes dev print a cd
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

# Cap on a single post-create command.
provision_timeout = "10m"

[stats]
# Record activity locally for "dev stats --heatmap".
sampler = true

# Also import durations from WakaTime, which covers editor time dev cannot see.
wakatime = false
wakatime_config = "~/.wakatime.cfg"
`
