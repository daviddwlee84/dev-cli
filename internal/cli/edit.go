package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/spf13/cobra"
)

// newEditCmd is the top-level `dev edit` convenience command.
func newEditCmd(app *App) *cobra.Command {
	return newConfigEditorCommand(app, "edit", "Open dev's config in $VISUAL or $EDITOR")
}

// newConfigEditCmd is the discoverable form next to config show/init/path.
func newConfigEditCmd(app *App) *cobra.Command {
	return newConfigEditorCommand(app, "edit", "Open this config in $VISUAL or $EDITOR")
}

func newConfigEditorCommand(app *App, use, short string) *cobra.Command {
	var editor string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: `Open the config file used by this invocation.

If it does not exist, first generate the same machine-detected, fully commented
template as "dev config init". Editor resolution is explicit:

    --editor → $VISUAL → $EDITOR → nvim → vim → vi

An editor value may contain arguments (for example "code --wait"); it is
executed through the user's shell with the config path safely quoted.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			proc, chosen, created, err := configEditorProcess(app, editor)
			if err != nil {
				return err
			}
			if created {
				path := app.configPath
				if path == "" {
					path = config.ConfigFile()
				}
				fmt.Fprintf(app.Err, "created %s from the detected machine layout\n", config.Contract(config.Expand(path)))
			}
			proc.Stdin, proc.Stdout, proc.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := proc.Run(); err != nil {
				return fmt.Errorf("editor %q: %w", chosen, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&editor, "editor", "", "editor command, overriding $VISUAL and $EDITOR")
	return cmd
}

// configEditorProcess resolves the effective config and editor without running
// it. The CLI runs the process directly; the TUI passes it to tea.ExecProcess,
// which suspends the alternate screen and redraws after the editor exits.
func configEditorProcess(app *App, editor string) (*exec.Cmd, string, bool, error) {
	path := app.configPath
	if path == "" {
		path = config.ConfigFile()
	}
	path = config.Expand(path)
	created, err := ensureConfigForEdit(path)
	if err != nil {
		return nil, "", false, err
	}
	proc, chosen, err := editorProcess(path, editor)
	return proc, chosen, created, err
}

// ensureConfigForEdit generates a usable config before opening an absent path.
// Editing an empty file would hide every default the user is trying to change.
func ensureConfigForEdit(path string) (bool, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		return false, fmt.Errorf("%s is a directory, not a config file", config.Contract(path))
	case err == nil:
		return false, nil
	case !os.IsNotExist(err):
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	body := renderStarterConfig(config.DetectLayout().Fallbacks())
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func editorProcess(path, override string) (*exec.Cmd, string, error) {
	chosen, err := resolveEditor(override)
	if err != nil {
		return nil, "", err
	}
	command := chosen + " " + shellQuote(path)
	return exec.Command(shellPath(), "-c", command), chosen, nil
}

func resolveEditor(override string) (string, error) {
	for _, candidate := range []string{override, os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if strings.TrimSpace(candidate) != "" {
			return candidate, nil
		}
	}
	for _, candidate := range []string{"nvim", "vim", "vi"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no editor found; set $VISUAL or $EDITOR, or pass --editor")
}
