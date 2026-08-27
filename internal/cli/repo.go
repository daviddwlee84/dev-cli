package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newRepoCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "List, clone, create and sync repositories",
		Long: `Manage the repositories under your scan roots, with gh or glab as the backend
for anything remote.

This is the "what projects do I have?" half of dev, kept separate from the
"what am I working on?" half that ls and the task commands answer.`,
	}
	cmd.AddCommand(
		newRepoListCmd(app),
		newRepoCloneCmd(app),
		newRepoOpenCmd(app),
		newRepoNewCmd(app),
		newRepoSyncCmd(app),
		newRepoRemoteCmd(app),
	)
	return cmd
}

// resolveRepoRef looks a repo up and renders ambiguity as a helpful error.
func resolveRepoRef(app *App, ref string) (repo.Repo, []repo.Repo, error) {
	return repo.Resolve(ctxOf(), app.Cfg.ScanRoots(), ref)
}

func newRepoListCmd(app *App) *cobra.Command {
	var (
		category  string
		dirtyOnly bool
		long      bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List every repository under the scan roots",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			// Reuse the TUI's bounded-parallel collector. The old serial
			// status/worktree/remote loop measured 4.2s over 56 repos.
			rows, err := collectRepos(ctx, app, runtime.None{})
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				fmt.Fprintf(app.Out, "No repositories under %s\n",
					strings.Join(contractAll(app.Cfg.ScanRoots()), ", "))
				return nil
			}

			t := NewTable("REPO", "CATEGORY", "BRANCH", "GIT", "LATEST", "WT", "PATH")
			for _, row := range rows {
				r := row.Repo
				if category != "" && !strings.EqualFold(r.Category, category) {
					continue
				}
				branch, gitCol := row.Status.Branch, row.Status.Summary()
				if r.Bare {
					branch, gitCol = "(bare)", "—"
				}
				if dirtyOnly && !row.Status.Dirty() {
					continue
				}
				wtCount := ""
				if row.Worktrees > 0 {
					wtCount = fmt.Sprintf("%d", row.Worktrees)
				}
				latest := "—"
				if !row.LastActivity.IsZero() {
					latest = humanAge(time.Since(row.LastActivity))
				}
				path := ""
				if long {
					path = config.Contract(r.Path)
				}
				t.Add(truncate(r.Name, 28), dash(r.Category), truncate(branch, 24),
					gitCol, latest, dash(wtCount), path)
			}
			if t.Len() == 0 {
				fmt.Fprintln(app.Out, "No repositories match that filter.")
				return nil
			}
			t.Render(app.Out)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&category, "category", "c", "", "only this category")
	f.BoolVar(&dirtyOnly, "dirty", false, "only repositories with uncommitted changes")
	f.BoolVarP(&long, "long", "l", false, "show full paths")
	return cmd
}

func newRepoCloneCmd(app *App) *cobra.Command {
	var (
		category string
		open     bool
	)
	cmd := &cobra.Command{
		Use:   "clone <owner/name|url>",
		Short: "Clone a repository into the right place under a scan root",
		Long: `Clone into <project_root>/<category>/<name>, so a new repo lands where the
rest of them live instead of wherever the shell happened to be.

Uses gh or glab when the host matches one, and plain git otherwise.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			ref := args[0]
			name := repoNameFromRef(ref)
			if name == "" {
				return fmt.Errorf("could not derive a directory name from %q", ref)
			}
			dest := filepath.Join(config.Expand(app.Cfg.Paths.ProjectRoot), category, name)
			if _, err := os.Stat(dest); err == nil {
				return fmt.Errorf("%s already exists", config.Contract(dest))
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}

			url := ref
			if k := forge.FromURL(ref); k != forge.Unknown {
				if f, err := forge.For(k); err == nil {
					url = f.CloneURL(ref)
				}
			} else if !strings.Contains(ref, "://") && !strings.HasPrefix(ref, "git@") {
				// A bare owner/name: let the first available forge decide.
				if f, err := forge.Preferred(); err == nil {
					url = f.CloneURL(ref)
				}
			}

			fmt.Fprintf(app.Out, "cloning %s → %s\n", url, config.Contract(dest))
			if _, err := gitx.Run(ctx, filepath.Dir(dest), "clone", url, dest); err != nil {
				return err
			}
			if open {
				rt := app.Runtime()
				if _, err := openCheckout(ctx, rt, dest, name); err != nil {
					app.warnf("could not open a session: %v", err)
				}
				if rt.Name() == "none" {
					app.cdDirective(dest)
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&category, "category", "c", "", "category subdirectory under project_root")
	f.BoolVarP(&open, "open", "o", false, "open the clone in the runtime afterwards")
	return cmd
}

func newRepoOpenCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "open <repo>",
		Short: "Open a repository in the runtime",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			r, _, err := resolveRepoRef(app, args[0])
			if err != nil {
				return err
			}
			rt := app.Runtime()
			handle, err := openCheckout(ctx, rt, r.Path, r.Name)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s  %s", r.Name, config.Contract(r.Path))
			if rt.Name() != "none" {
				fmt.Fprintf(app.Out, "  (%s %s)", rt.Name(), handle)
			}
			fmt.Fprintln(app.Out)
			if rt.Name() == "none" {
				app.cdDirective(r.Path)
			}
			return nil
		},
	}
}

func newRepoNewCmd(app *App) *cobra.Command {
	var (
		category string
		private  bool
		remote   bool
		desc     string
	)
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a new local repository (optionally with a remote)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			name := args[0]
			dest := filepath.Join(config.Expand(app.Cfg.Paths.ProjectRoot), category, name)
			if _, err := os.Stat(dest); err == nil {
				return fmt.Errorf("%s already exists", config.Contract(dest))
			}
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			if _, err := gitx.Run(ctx, dest, "init", "-b", "main"); err != nil {
				return err
			}
			readme := filepath.Join(dest, "README.md")
			if err := os.WriteFile(readme, []byte("# "+name+"\n"), 0o644); err != nil {
				return err
			}
			if _, err := gitx.Run(ctx, dest, "add", "README.md"); err != nil {
				return err
			}
			if _, err := gitx.Run(ctx, dest, "commit", "-m", "chore: initial commit"); err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "created %s\n", config.Contract(dest))

			if remote {
				f, err := forge.Preferred()
				if err != nil {
					app.warnf("%v — the local repo is ready; add a remote by hand", err)
					return nil
				}
				url, err := f.CreateRepo(ctx, dest, forge.RepoRequest{
					Name: name, Description: desc, Private: private, Push: true,
				})
				if err != nil {
					return err
				}
				fmt.Fprintf(app.Out, "remote  %s\n", url)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&category, "category", "c", "", "category subdirectory under project_root")
	f.BoolVar(&private, "private", true, "create the remote as private")
	f.BoolVar(&remote, "remote", false, "also create a remote repository with gh or glab")
	f.StringVar(&desc, "description", "", "repository description")
	return cmd
}

func newRepoSyncCmd(app *App) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "sync [repo]",
		Short: "Fetch and prune, reporting what moved",
		Long: `Fetch from origin and prune deleted remote-tracking refs.

Deliberately not a pull: dev reports how each branch stands and leaves the
decision to you, because a pull that quietly merges or rebases is how a
history gets a shape nobody intended.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			var targets []repo.Repo
			switch {
			case all:
				var err error
				targets, err = repo.Discover(ctx, app.Cfg.ScanRoots(), repo.DefaultOptions())
				if err != nil {
					return err
				}
			case len(args) == 1:
				r, _, err := resolveRepoRef(app, args[0])
				if err != nil {
					return err
				}
				targets = []repo.Repo{r}
			default:
				path, name, err := repoContext(app, "")
				if err != nil {
					return err
				}
				targets = []repo.Repo{{Name: name, Path: path}}
			}

			t := NewTable("REPO", "BRANCH", "GIT", "NOTE")
			for _, r := range targets {
				if r.Bare {
					continue
				}
				note := ""
				if _, err := gitx.Run(ctx, r.Path, "fetch", "--prune", "--quiet", "origin"); err != nil {
					note = "fetch failed (no remote?)"
				}
				st, err := gitx.StatusOf(ctx, r.Path)
				if err != nil {
					continue
				}
				if note == "" {
					switch {
					case st.Behind > 0 && st.Ahead > 0:
						note = "diverged — rebase or merge explicitly"
					case st.Behind > 0 && !st.Dirty():
						note = "behind — git pull --ff-only"
					case st.Behind > 0:
						note = "behind, and dirty — commit first"
					case st.Ahead > 0:
						note = "unpushed commits"
					}
				}
				t.Add(truncate(r.Name, 28), truncate(st.Branch, 24), st.Summary(), note)
			}
			t.Render(app.Out)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "every repository under the scan roots")
	return cmd
}

// repoNameFromRef derives a directory name from a clone reference.
func repoNameFromRef(ref string) string {
	ref = strings.TrimSuffix(strings.TrimSuffix(ref, "/"), ".git")
	if i := strings.LastIndexAny(ref, "/:"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

func contractAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = config.Contract(p)
	}
	return out
}

func newRepoRemoteCmd(app *App) *cobra.Command {
	var (
		jsonOut    bool
		cachedOnly bool
		limit      int
	)
	cmd := &cobra.Command{
		Use:     "remote [query]",
		Aliases: []string{"search"},
		Short:   "List and search repositories visible through gh and glab",
		Long: `Combine the repositories visible to the authenticated gh and glab CLIs, and
mark those already cloned under paths.scan_roots.

The TUI exposes the same data in its REMOTE tab and filters it live with /.
This command is the non-interactive form for scripts, pipes and terminals where
a full-screen UI is not wanted. Use --cached for an instant, offline result;
the cache is private and expires according to forge.cache_ttl.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var (
				rows []tui.RemoteRow
				err  error
			)
			if cachedOnly {
				var ok bool
				rows, ok = cachedRemotes(ctxOf(), app)
				if !ok {
					return fmt.Errorf("no fresh remote cache; run `dev repo remote` once online")
				}
			} else {
				rows, err = collectRemotes(ctxOf(), app, limit)
			}
			// Partial results are useful when one forge is authenticated and
			// the other is not. Render them, then report the partial failure.
			if len(rows) == 0 && err != nil {
				return err
			}
			query := ""
			if len(args) == 1 {
				query = strings.ToLower(args[0])
			}
			var filtered []tui.RemoteRow
			for _, r := range rows {
				hay := strings.ToLower(strings.Join([]string{
					string(r.Repo.Forge), r.Repo.FullName, r.Repo.Description,
					r.Repo.Visibility, r.LocalName,
				}, " "))
				matched := true
				for _, term := range strings.Fields(query) {
					if !strings.Contains(hay, term) {
						matched = false
						break
					}
				}
				if matched {
					filtered = append(filtered, r)
				}
			}

			if jsonOut {
				enc := json.NewEncoder(app.Out)
				enc.SetIndent("", "  ")
				if encodeErr := enc.Encode(filtered); encodeErr != nil {
					return encodeErr
				}
			} else {
				t := NewTable("FORGE", "REPOSITORY", "VIS", "LOCAL", "UPDATED", "DESCRIPTION")
				for _, r := range filtered {
					local := "—"
					if r.LocalPath != "" {
						local = config.Contract(r.LocalPath)
					}
					updated := "—"
					if !r.Repo.UpdatedAt.IsZero() {
						updated = r.Repo.UpdatedAt.Format("2006-01-02")
					}
					t.Add(string(r.Repo.Forge), truncate(r.Repo.FullName, 36),
						strings.ToLower(r.Repo.Visibility), truncate(local, 30), updated,
						truncate(r.Repo.Description, 50))
				}
				if t.Len() == 0 {
					fmt.Fprintln(app.Out, "No remote repositories match that query.")
				} else {
					t.Render(app.Out)
				}
			}
			if err != nil {
				app.warnf("partial remote results: %v", err)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&jsonOut, "json", false, "emit JSON")
	f.BoolVar(&cachedOnly, "cached", false, "use the fresh XDG cache without querying either forge")
	f.IntVar(&limit, "limit", 0, "maximum repositories requested from each forge (default: config forge.remote_limit)")
	return cmd
}
