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
			path := app.configPath
			if path == "" {
				path = config.ConfigFile()
			}
			path = config.Expand(path)
			if err := ensureConfigForEdit(app, path); err != nil {
				return err
			}

			chosen, err := resolveEditor(editor)
			if err != nil {
				return err
			}
			command := chosen + " " + shellQuote(path)
			proc := exec.Command(shellPath(), "-c", command)
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

// ensureConfigForEdit generates a usable config before opening an absent path.
// Editing an empty file would hide every default the user is trying to change.
func ensureConfigForEdit(app *App, path string) error {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		return fmt.Errorf("%s is a directory, not a config file", config.Contract(path))
	case err == nil:
		return nil
	case !os.IsNotExist(err):
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := renderStarterConfig(config.DetectLayout().Fallbacks())
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(app.Err, "created %s from the detected machine layout\n", config.Contract(path))
	return nil
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
