package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/spf13/cobra"
)

func newTryCmd(app *App) *cobra.Command {
	var (
		list  bool
		clone string
		noGit bool
	)
	cmd := &cobra.Command{
		Use:   "try [name]",
		Short: "Make a dated scratch directory for an experiment",
		Long: `Create (or jump to) a dated experiment directory under paths.tries_root.

Experiments need a home that is neither /tmp (where they vanish) nor your
projects directory (where they accumulate as clutter you are afraid to delete).
A try is date-prefixed, disposable by default, and promotable with
"dev graduate" when it turns out to be real.

  dev try                    list what is there
  dev try redis-streams      jump to a matching try, or create 2026-08-27-redis-streams
  dev try --clone <url>      clone a repo into a dated try directory`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			root := config.Expand(app.Cfg.Paths.TriesRoot)
			if err := os.MkdirAll(root, 0o755); err != nil {
				return err
			}

			if list || (len(args) == 0 && clone == "") {
				return listTries(app, root)
			}

			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if clone != "" && name == "" {
				name = repoNameFromRef(clone)
			}
			slug := config.Slug(strings.ReplaceAll(name, " ", "-"))

			// An existing try wins over creating a near-duplicate: the whole
			// point is to stop accumulating test/test2/test-actually.
			if match := findTry(root, slug); match != "" && clone == "" {
				fmt.Fprintf(app.Out, "%s\n", config.Contract(match))
				return openOrCD(app, ctx, match, filepath.Base(match))
			}

			dir := filepath.Join(root, time.Now().Format("2006-01-02")+"-"+slug)
			if _, err := os.Stat(dir); err == nil {
				fmt.Fprintf(app.Out, "%s\n", config.Contract(dir))
				return openOrCD(app, ctx, dir, filepath.Base(dir))
			}

			if clone != "" {
				url := clone
				if k := forge.FromURL(clone); k != forge.Unknown {
					if f, err := forge.For(k); err == nil {
						url = f.CloneURL(clone)
					}
				}
				if _, err := gitx.Run(ctx, root, "clone", url, dir); err != nil {
					return err
				}
			} else {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				if !noGit {
					// A try with git history can be graduated without losing
					// the path that got there, which is most of its value.
					if _, err := gitx.Run(ctx, dir, "init", "-b", "main"); err != nil {
						app.warnf("could not git init: %v", err)
					}
				}
			}
			fmt.Fprintf(app.Out, "%s\n", config.Contract(dir))
			return openOrCD(app, ctx, dir, filepath.Base(dir))
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&list, "list", "l", false, "list existing tries")
	f.StringVar(&clone, "clone", "", "clone a repository into the new try")
	f.BoolVar(&noGit, "no-git", false, "do not git init the new directory")
	return cmd
}

// tryEntry is one scratch directory with the facts needed to rank it.
type tryEntry struct {
	name     string
	path     string
	modified time.Time
	isRepo   bool
	dirty    bool
}

func listTries(app *App, root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var tries []tryEntry
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(root, e.Name())
		t := tryEntry{name: e.Name(), path: path, modified: info.ModTime()}
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			t.isRepo = true
			if st, err := gitx.StatusOf(ctxOf(), path); err == nil {
				t.dirty = st.Dirty()
			}
		}
		tries = append(tries, t)
	}
	if len(tries) == 0 {
		fmt.Fprintf(app.Out, "No tries yet under %s\n", config.Contract(root))
		return nil
	}
	// Most recently touched first: "what was I doing yesterday?" is the
	// question a list of experiments actually gets asked.
	sort.Slice(tries, func(i, j int) bool { return tries[i].modified.After(tries[j].modified) })

	tbl := NewTable("TRY", "AGE", "GIT")
	for _, t := range tries {
		gitCol := "—"
		switch {
		case t.isRepo && t.dirty:
			gitCol = "repo ●"
		case t.isRepo:
			gitCol = "repo"
		}
		tbl.Add(truncate(t.name, 44), humanAge(time.Since(t.modified)), gitCol)
	}
	tbl.Render(app.Out)
	fmt.Fprintf(app.Err, "\n%d tries under %s — promote one with `dev graduate <try>`\n",
		len(tries), config.Contract(root))
	return nil
}

// findTry locates an existing try whose name contains the slug, preferring the
// most recent when several match.
func findTry(root, slug string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var best string
	var bestTime time.Time
	for _, e := range entries {
		if !e.IsDir() || !strings.Contains(strings.ToLower(e.Name()), strings.ToLower(slug)) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			best, bestTime = filepath.Join(root, e.Name()), info.ModTime()
		}
	}
	return best
}

func openOrCD(app *App, ctx context.Context, dir, label string) error {
	rt := app.Runtime()
	if rt.Name() == "none" {
		app.cdDirective(dir)
		return nil
	}
	if _, err := openCheckout(ctx, rt, dir, label); err != nil {
		app.warnf("could not open a session: %v", err)
	}
	return nil
}
