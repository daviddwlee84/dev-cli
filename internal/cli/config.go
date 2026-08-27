package cli

import (
	"fmt"
	"os"
	"path/filepath"

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
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented starter config.toml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := app.configPath
			if path == "" {
				path = config.ConfigFile()
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", config.Contract(path))
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(starterConfig), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "wrote %s\n", config.Contract(path))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite an existing config")
	return cmd
}

const starterConfig = `# dev configuration — every value here is the built-in default.
# Delete anything you do not want to override.

[paths]
# Where dev looks for repositories. Each root is scanned to a depth of 3, so
# both <root>/<Repo> and <root>/<Category>/<Repo> are found.
scan_roots = ["~/Documents/Program", "~/src/tries"]

# Where "dev try" creates dated experiment directories.
tries_root = "~/src/tries"

# Where "dev repo clone|new" and "dev graduate" place real projects.
project_root = "~/Documents/Program"

# Where linked worktrees live. Keep this OUTSIDE any repository: a checkout
# nested inside another checkout makes every indexer, file watcher and ripgrep
# run see a second copy of the tree.
#
# Put it wherever suits the machine — a faster volume, a larger disk:
#   worktree_root = "/mnt/fast/worktrees"
worktree_root = "~/Worktrees"

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
