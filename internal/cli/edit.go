package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/daviddwlee84/dev-cli/internal/tui"
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
	var project bool
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
			if project {
				root, err := resolveSetupRepo(app, nil)
				if err != nil {
					return err
				}
				path, err := projectconfig.ConfigPath(root)
				if err != nil {
					return err
				}
				if _, err := os.Stat(path); os.IsNotExist(err) {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						return err
					}
					body := "# Project-owned dev overrides. Host paths/runtime/state are not allowed here.\nversion = 1\n\n[worktree]\n"
					if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
						return err
					}
					fmt.Fprintf(app.Err, "created %s\n", config.Contract(path))
				}
				proc, chosen, err := editorProcess(path, editor)
				if err != nil {
					return err
				}
				proc.Stdin, proc.Stdout, proc.Stderr = os.Stdin, os.Stdout, os.Stderr
				if err := proc.Run(); err != nil {
					return fmt.Errorf("editor %q: %w", chosen, err)
				}
				return nil
			}
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
	cmd.Flags().BoolVar(&project, "project", false, "edit .dev-cli/config.toml in the current repository")
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

func prepareTUICapabilityEdit(path string) (tui.CapabilityEdit, error) {
	logical := filepath.Clean(path)
	resolved, err := pathx.Canonical(logical)
	if err != nil {
		return tui.CapabilityEdit{}, fmt.Errorf("resolve capability file %s: %w", config.Contract(logical), err)
	}
	root, rootInfo, err := safefile.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return tui.CapabilityEdit{}, fmt.Errorf("open capability file directory %s: %w", config.Contract(logical), err)
	}
	completeOwnsRoot := false
	defer func() {
		if !completeOwnsRoot {
			_ = root.Close()
		}
	}()
	original, observed, err := safefile.ReadStableRegular(
		context.Background(), root, filepath.Base(resolved), nil, safefile.CompiledMaxFileBytes,
	)
	if err != nil {
		return tui.CapabilityEdit{}, fmt.Errorf("read capability file %s: %w", config.Contract(logical), err)
	}
	originalSize := len(original)
	originalDigest := sha256.Sum256(original)

	working, err := os.CreateTemp("", "dev-capability-edit-*")
	if err != nil {
		return tui.CapabilityEdit{}, err
	}
	workingPath := working.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(workingPath)
		}
	}()
	if _, err := working.Write(original); err != nil {
		_ = working.Close()
		return tui.CapabilityEdit{}, err
	}
	if err := working.Sync(); err != nil {
		_ = working.Close()
		return tui.CapabilityEdit{}, err
	}
	if err := working.Close(); err != nil {
		return tui.CapabilityEdit{}, err
	}
	original = nil
	process, _, err := editorProcess(workingPath, "")
	if err != nil {
		return tui.CapabilityEdit{}, err
	}
	cleanup = false
	completeOwnsRoot = true

	return tui.CapabilityEdit{
		Command: process,
		Complete: func(runErr error) error {
			defer root.Close()
			if runErr != nil {
				return fmt.Errorf("capability editor failed; working copy preserved at %s: %w", config.Contract(workingPath), runErr)
			}
			edited, err := safefile.ReadRegular(context.Background(), workingPath, safefile.CompiledMaxFileBytes)
			if err != nil {
				return fmt.Errorf("read edited capability file; working copy preserved at %s: %w", config.Contract(workingPath), err)
			}
			if len(edited) == originalSize && sha256.Sum256(edited) == originalDigest {
				return os.Remove(workingPath)
			}
			currentResolved, err := pathx.Canonical(logical)
			if err != nil || !sameCleanPath(currentResolved, resolved) {
				return fmt.Errorf("capability file target changed; working copy preserved at %s: %w",
					config.Contract(workingPath), errors.Join(err, safefile.ErrChanged))
			}
			if err := safefile.VerifyRoot(filepath.Dir(resolved), rootInfo); err != nil {
				return fmt.Errorf("capability file directory changed; working copy preserved at %s: %w",
					config.Contract(workingPath), err)
			}
			if _, err := safefile.AtomicReplace(
				context.Background(), root, filepath.Base(resolved), observed, edited, observed.Mode().Perm(),
			); err != nil {
				return fmt.Errorf("replace changed capability file; working copy preserved at %s: %w", config.Contract(workingPath), err)
			}
			return os.Remove(workingPath)
		},
	}, nil
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
	command, err := fleetEditorCommand(chosen, path)
	return command, chosen, err
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
