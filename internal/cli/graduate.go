package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/spf13/cobra"
)

// datePrefix matches the "2026-08-27-" a try directory carries.
var datePrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-`)

func newGraduateCmd(app *App) *cobra.Command {
	var (
		category string
		name     string
		private  bool
		remote   bool
		push     bool
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "graduate [try]",
		Short: "Promote an experiment into a real project",
		Long: `Move a try out of the scratch directory and into your projects tree.

An experiment that turns out to matter should stop being an experiment, and
that transition is where things usually get lost — the directory keeps its date
prefix forever, or it gets copied by hand and the history is left behind.

graduate moves the directory to <project_root>/<category>/<name>, drops the
date prefix, makes sure it has a git repo and a first commit, and optionally
creates the remote with gh or glab.

With no argument, the try containing the current directory is used.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			triesRoot := config.Expand(app.Cfg.Paths.TriesRoot)

			src, err := resolveTry(app, triesRoot, args)
			if err != nil {
				return err
			}
			if name == "" {
				name = datePrefix.ReplaceAllString(filepath.Base(src), "")
			}
			if name == "" {
				return fmt.Errorf("could not derive a project name from %s — pass --name", src)
			}

			dest := filepath.Join(config.Expand(app.Cfg.Paths.ProjectRoot), category, name)
			if _, err := os.Stat(dest); err == nil {
				return fmt.Errorf("%s already exists", config.Contract(dest))
			}

			fmt.Fprintf(app.Out, "graduate  %s\n", config.Contract(src))
			fmt.Fprintf(app.Out, "       →  %s\n", config.Contract(dest))
			if dryRun {
				fmt.Fprintln(app.Out, "\n(dry run — nothing moved)")
				return nil
			}

			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			// Rename first: it is atomic within a filesystem and leaves
			// nothing half-copied if it fails.
			if err := os.Rename(src, dest); err != nil {
				return fmt.Errorf("move %s: %w\n"+
					"If the projects root is on another filesystem, copy it by hand and remove the try",
					config.Contract(src), err)
			}

			if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
				if _, err := gitx.Run(ctx, dest, "init", "-b", "main"); err != nil {
					return err
				}
				fmt.Fprintln(app.Out, "   git init")
			}
			// An experiment usually has files but no commit. A project needs
			// one: without a HEAD there is no default branch, so `dev start`
			// would have nothing to base a worktree on.
			if made, err := ensureInitialCommit(ctx, dest, name); err != nil {
				app.warnf("could not make an initial commit: %v", err)
			} else if made {
				fmt.Fprintln(app.Out, "   committed the existing work")
			}

			if remote {
				f, err := forge.Preferred()
				if err != nil {
					app.warnf("%v — the project is in place; add a remote by hand", err)
				} else {
					url, err := f.CreateRepo(ctx, dest, forge.RepoRequest{
						Name: name, Private: private, Push: push,
					})
					if err != nil {
						app.warnf("could not create the remote: %v", err)
					} else {
						fmt.Fprintf(app.Out, "   remote    %s\n", url)
					}
				}
			}

			fmt.Fprintf(app.Out, "\n%s is now a project. Start work on it with:\n  dev start %s --task <name>\n",
				name, name)
			return openOrCD(app, ctx, dest, name)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&category, "category", "c", "", "category subdirectory under project_root")
	f.StringVar(&name, "name", "", "project name (default: the try name without its date prefix)")
	f.BoolVar(&private, "private", true, "create the remote as private")
	f.BoolVar(&remote, "remote", false, "create a remote repository with gh or glab")
	f.BoolVar(&push, "push", true, "push after creating the remote")
	f.BoolVar(&dryRun, "dry-run", false, "show what would happen without moving anything")
	return cmd
}

// resolveTry finds the try directory to graduate: an explicit reference, or
// the try containing the current directory.
func resolveTry(app *App, triesRoot string, args []string) (string, error) {
	if len(args) == 1 {
		ref := args[0]
		if abs := config.Expand(ref); isDir(abs) {
			return abs, nil
		}
		direct := filepath.Join(triesRoot, ref)
		if isDir(direct) {
			return direct, nil
		}
		if match := findTry(triesRoot, ref); match != "" {
			return match, nil
		}
		return "", fmt.Errorf("no try matching %q under %s", ref, config.Contract(triesRoot))
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(cwd, triesRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is not inside %s — name the try to graduate",
			config.Contract(cwd), config.Contract(triesRoot))
	}
	// Walk up to the try's own top-level directory, so running this from a
	// subdirectory still graduates the whole experiment.
	rel, _ := filepath.Rel(triesRoot, cwd)
	first := strings.Split(rel, string(filepath.Separator))[0]
	return filepath.Join(triesRoot, first), nil
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// ensureInitialCommit gives a repository a first commit if it has none,
// seeding a README when the tree is otherwise empty. Reports whether it made
// one.
func ensureInitialCommit(ctx context.Context, dir, name string) (bool, error) {
	if _, _, err := gitx.LastCommit(ctx, dir); err == nil {
		return false, nil // already has history
	}
	if empty, err := isEmptyTree(dir); err != nil {
		return false, err
	} else if empty {
		readme := filepath.Join(dir, "README.md")
		if err := os.WriteFile(readme, []byte("# "+name+"\n"), 0o644); err != nil {
			return false, err
		}
	}
	if _, err := gitx.Run(ctx, dir, "add", "--all"); err != nil {
		return false, err
	}
	if _, err := gitx.Run(ctx, dir, "commit", "-m", "chore: graduate experiment into a project"); err != nil {
		return false, err
	}
	return true, nil
}

// isEmptyTree reports a directory with nothing in it but git's own metadata.
func isEmptyTree(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			return false, nil
		}
	}
	return true, nil
}
