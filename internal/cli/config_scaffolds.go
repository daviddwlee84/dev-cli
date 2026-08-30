package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
	"github.com/spf13/cobra"
)

func newConfigShowCmd(app *App) *cobra.Command {
	var projectOnly bool
	cmd := &cobra.Command{
		Use: "show [repo]", Short: "Print the effective global or project configuration",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !projectOnly && len(args) == 0 {
				source := app.Cfg.Source
				if source == "" {
					source = "built-in defaults (no config.toml)"
				}
				fmt.Fprintf(app.Err, "# source: %s\n", config.Contract(source))
				return toml.NewEncoder(app.Out).Encode(app.Cfg)
			}
			root, err := resolveSetupRepo(app, args)
			if err != nil {
				return err
			}
			project, err := projectconfig.Load(root, legacyProjectLayer(root))
			if err != nil {
				return err
			}
			for _, layer := range project.Layers {
				fmt.Fprintf(app.Err, "# project source: %s\n", config.Contract(layer))
			}
			for _, diagnostic := range project.Diagnostics {
				fmt.Fprintf(app.Err, "# %s: %s\n", diagnostic.Kind, diagnostic.Message)
			}
			return toml.NewEncoder(app.Out).Encode(project.Effective)
		},
	}
	cmd.Flags().BoolVar(&projectOnly, "project", false, "show the current/selected repository overlay")
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
}

func newConfigPathCmd(app *App) *cobra.Command {
	var projectOnly bool
	cmd := &cobra.Command{
		Use: "path [repo]", Short: "Print the global or project config file path",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !projectOnly && len(args) == 0 {
				path := app.configPath
				if path == "" {
					path = config.ConfigFile()
				}
				fmt.Fprintln(app.Out, config.Expand(path))
				return nil
			}
			root, err := resolveSetupRepo(app, args)
			if err != nil {
				return err
			}
			path, err := projectconfig.ConfigPath(root)
			if err != nil {
				return err
			}
			fmt.Fprintln(app.Out, path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&projectOnly, "project", false, "print the current/selected repository config path")
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
}

const starterScaffolds = `# Repository bootstrap presets for dev repo new/setup.
version = 1
default_preset = "agent-ready"
default_agents = ["claude-code", "codex"]

# Built-in presets "minimal" and "agent-ready" are always available.
# Add machine-specific presets here, for example:
#
# [presets.go-agent]
# extends = "agent-ready"
# description = "Go module with agent-ready repository metadata"
# gitignore = ["go"]
# initial_check_in = "stage" # commit, stage, or none
#
# A starter catalog can also provide the initial filesystem snapshot:
# template = "owner/starter-catalog"
# template_ref = "v2"
# template_subdir = "services/go"
#
# [[presets.go-agent.inputs]]
# id = "module"
# type = "string"
# label = "Go module path"
# required = true
#
# [[presets.go-agent.hooks]]
# id = "go-mod-init"
# phase = "before_commit"
# command = ["go", "mod", "init", "{{input.module}}"]
# required = true
`

func newConfigScaffoldsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "scaffolds", Short: "Show, edit, initialise and locate repository scaffold presets"}
	cmd.AddCommand(
		newConfigScaffoldsInitCmd(app),
		&cobra.Command{
			Use: "show", Short: "Print the effective scaffold catalog", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				catalog, _, err := loadRepoScaffoldConfig(app, "", false)
				if err != nil {
					return err
				}
				return toml.NewEncoder(app.Out).Encode(catalog)
			},
		},
		&cobra.Command{
			Use: "path", Short: "Print the scaffold config path", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Fprintln(app.Out, scaffoldConfigPath(app))
				return nil
			},
		},
		newConfigScaffoldsEditCmd(app),
	)
	return cmd
}

func newConfigScaffoldsInitCmd(app *App) *cobra.Command {
	var force, stdout bool
	cmd := &cobra.Command{
		Use: "init", Short: "Write a starter scaffolds.toml", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if stdout {
				fmt.Fprint(app.Out, starterScaffolds)
				return nil
			}
			path := scaffoldConfigPath(app)
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force or --stdout)", config.Contract(path))
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(starterScaffolds), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "wrote %s\n", config.Contract(path))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite an existing file")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "print instead of writing")
	return cmd
}

func newConfigScaffoldsEditCmd(app *App) *cobra.Command {
	var editor string
	cmd := &cobra.Command{
		Use: "edit", Short: "Open scaffolds.toml in an editor", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := scaffoldConfigPath(app)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte(starterScaffolds), 0o644); err != nil {
					return err
				}
			}
			process, chosen, err := editorProcess(path, editor)
			if err != nil {
				return err
			}
			process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
			if err := process.Run(); err != nil {
				return fmt.Errorf("editor %q: %w", chosen, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&editor, "editor", "", "editor command, overriding $VISUAL and $EDITOR")
	return cmd
}

func scaffoldConfigPath(app *App) string {
	if app.scaffoldsPath != "" {
		return config.Expand(app.scaffoldsPath)
	}
	return config.ScaffoldsFile()
}

func newConfigTrustCmd(app *App) *cobra.Command {
	var yes, revoke, list bool
	cmd := &cobra.Command{
		Use: "trust [repo]", Short: "Approve or revoke executable project configuration by content hash",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := projectconfig.NewTrustStore(filepath.Join(app.Cfg.StateDir(), "trust", "project-config-v1.json"))
			if list {
				records, err := store.List()
				if err != nil {
					return err
				}
				for _, record := range records {
					fmt.Fprintf(app.Out, "%s  %s  %s\n", config.Contract(record.Repository), record.ExecutionHash, record.ApprovedAt.Format("2006-01-02T15:04:05Z"))
				}
				return nil
			}
			root, err := resolveSetupRepo(app, args)
			if err != nil {
				return err
			}
			if revoke {
				removed, err := store.Revoke(ctxOf(), root)
				if err != nil {
					return err
				}
				if removed {
					fmt.Fprintf(app.Out, "revoked project config trust for %s\n", config.Contract(root))
				} else {
					fmt.Fprintln(app.Out, "no matching trust record")
				}
				return nil
			}
			project, err := projectconfig.Load(root, legacyProjectLayer(root))
			if err != nil {
				return err
			}
			if !project.RequiresTrust() {
				fmt.Fprintln(app.Out, "This repository has no executable project configuration to trust.")
				return nil
			}
			fmt.Fprintf(app.Out, "repository  %s\nhash        %s\n", config.Contract(root), project.ExecutionHash)
			if !yes {
				if !app.interactive() {
					return fmt.Errorf("non-interactive trust requires --yes")
				}
				if !confirm(app, bufio.NewReader(app.In), "trust this exact executable project configuration hash") {
					fmt.Fprintln(app.Out, "not trusted")
					return nil
				}
			}
			if _, err := store.Approve(ctxOf(), root, project.ExecutionHash); err != nil {
				return err
			}
			fmt.Fprintln(app.Out, "trusted")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "approve without prompting")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "remove trust for this repository")
	cmd.Flags().BoolVar(&list, "list", false, "list trusted repository hashes")
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
}
