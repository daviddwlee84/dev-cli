package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newRepoCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "List, clone, create and sync repositories",
		Long: `Manage the repositories under your scan roots, using forge CLIs for remote
inventory, cloning and review operations.

This is the "what projects do I have?" half of dev, kept separate from the
"what am I working on?" half that ls and the task commands answer.`,
	}
	cmd.AddCommand(
		newRepoListCmd(app),
		newRepoMarkCmd(app),
		newRepoContextCmd(app),
		newRepoCloneCmd(app),
		newRepoOpenCmd(app),
		newRepoNewCmd(app),
		newRepoSyncCmd(app),
		newRepoRemoteCmd(app),
	)
	return cmd
}

func newRepoContextCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "context [repo]",
		Short: "Print agent-ready Git, worktree, runtime and task context",
		Long: `Print a deterministic Markdown handoff for one repository.

The context includes the canonical checkout, every linked worktree, Git state,
runtime and agent sessions, and tracked task state. With no repository argument,
the repository containing the current directory is used; standing in a linked
worktree still reports the whole repository.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			var r repo.Repo
			if len(args) == 1 {
				resolved, _, err := resolveRepoRef(app, args[0])
				if err != nil {
					return err
				}
				r = resolved
			} else {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				g, err := gitx.Discover(ctx, cwd)
				if err != nil {
					return fmt.Errorf("%s is not a git repository — pass a repo", config.Contract(cwd))
				}
				r = repo.Repo{
					Name: g.Name, Path: g.MainRoot, RealPath: g.MainRoot,
					CommonDir: g.GitCommonDir, HasGit: true, Bare: g.Bare,
				}
			}

			tasks, err := app.Tasks.List()
			if err != nil {
				return err
			}
			tasks = tasksForRepo(tasks, r)
			rt := app.Runtime()
			sessions, _ := rt.List(ctx)
			context := inventory.CollectRepoContext(ctx, r, tasks, sessions, rt.Name())
			fmt.Fprint(app.Out, inventory.FormatRepoContext(context, -1))
			return nil
		},
	}
}

func tasksForRepo(tasks []*task.Task, r repo.Repo) []*task.Task {
	var out []*task.Task
	for _, t := range tasks {
		if sameCleanPath(t.RepoPath, r.Path) || sameCleanPath(t.RepoPath, r.RealPath) {
			out = append(out, t)
		}
	}
	return out
}

func sameCleanPath(a, b string) bool {
	return a != "" && b != "" && filepath.Clean(a) == filepath.Clean(b)
}

// resolveRepoRef looks a repo up and renders ambiguity as a helpful error.
func resolveRepoRef(app *App, ref string) (repo.Repo, []repo.Repo, error) {
	return repo.Resolve(ctxOf(), app.Cfg.DiscoveryRoots(), ref)
}

func newRepoListCmd(app *App) *cobra.Command {
	var (
		category          string
		dirtyOnly         bool
		long              bool
		jsonOut           bool
		includeTries      bool
		noRemote          bool
		localOnly         bool
		multipleRemotes   bool
		multipleUpstreams bool
		sizes             bool
		refreshSizes      bool
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
			rows, err := collectReposWithOptions(ctx, app, runtime.None{}, repoCollectOptions{
				IncludeTries: includeTries,
			})
			if err != nil {
				return err
			}
			filtered := make([]tui.RepoRow, 0, len(rows))
			for _, row := range rows {
				if category != "" && !strings.EqualFold(row.Repo.Category, category) {
					continue
				}
				if dirtyOnly && !row.Status.Dirty() {
					continue
				}
				if noRemote && (row.TopologyErr != nil || row.Topology.HasRemote()) {
					continue
				}
				if localOnly && (row.TopologyErr != nil || len(row.Topology.LocalOnlyBranches) == 0) {
					continue
				}
				if multipleRemotes && (row.TopologyErr != nil || !row.Topology.MultipleRemotes()) {
					continue
				}
				if multipleUpstreams && (row.TopologyErr != nil || !row.Topology.MultipleUpstreams()) {
					continue
				}
				filtered = append(filtered, row)
			}
			if sizes || refreshSizes {
				measureRepoRows(ctx, app, filtered, refreshSizes)
			}
			if jsonOut {
				encoded := make([]repoListJSONRow, 0, len(filtered))
				for _, row := range filtered {
					encoded = append(encoded, makeRepoListJSONRow(row))
				}
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(encoded)
			}
			if len(filtered) == 0 {
				if len(rows) == 0 {
					fmt.Fprintf(app.Out, "No repositories under %s\n",
						strings.Join(contractAll(app.Cfg.DiscoveryRoots()), ", "))
				} else {
					fmt.Fprintln(app.Out, "No repositories match that filter.")
				}
				return nil
			}

			headings := []string{"REPO"}
			if includeTries {
				headings = append(headings, "KIND")
			}
			headings = append(headings, "CATEGORY", "BRANCH", "GIT", "REMOTE")
			if sizes || refreshSizes {
				headings = append(headings, "SIZE")
			}
			headings = append(headings, "LATEST", "WT", "PATH")
			t := app.newTable(headings...)
			style := app.outStyle()
			for _, row := range filtered {
				r := row.Repo
				branch, gitCol := row.Status.Branch, row.Status.Summary()
				if r.Bare {
					branch, gitCol = "(bare)", "—"
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
				remoteColumn := row.Topology.Summary()
				if row.TopologyErr != nil {
					remoteColumn = "?"
				}
				values := []string{truncate(r.Name, 28)}
				if includeTries {
					kind := catalog.KindRepository
					if row.Asset != nil {
						kind = row.Asset.Kind
					}
					values = append(values, string(kind))
				}
				values = append(values, dash(r.Category), truncate(branch, 24), style.git(gitCol), style.git(truncate(remoteColumn, 28)))
				if sizes || refreshSizes {
					values = append(values, sizeColumn(row.Usage, row.SizeError))
				}
				values = append(values, latest, dash(wtCount), path)
				t.Add(values...)
			}
			t.Render(app.Out)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&category, "category", "c", "", "only this category")
	f.BoolVar(&dirtyOnly, "dirty", false, "only repositories with uncommitted changes")
	f.BoolVarP(&long, "long", "l", false, "show full paths")
	f.BoolVar(&jsonOut, "json", false, "emit stable JSON")
	f.BoolVar(&includeTries, "include-tries", false, "include active and deprecated Try repositories")
	f.BoolVar(&noRemote, "no-remote", false, "only repositories with no configured Git remote")
	f.BoolVar(&localOnly, "local-only", false, "only repositories with a branch lacking a remote-backed upstream")
	f.BoolVar(&multipleRemotes, "multiple-remotes", false, "only repositories with multiple configured remotes")
	f.BoolVar(&multipleUpstreams, "multiple-upstreams", false, "only repositories whose branches track multiple remotes")
	f.BoolVar(&sizes, "sizes", false, "measure logical checkout/private/shared Git bytes")
	f.BoolVar(&refreshSizes, "refresh-sizes", false, "ignore the size cache and measure again")
	return cmd
}

func newRepoMarkCmd(app *App) *cobra.Command {
	var add, remove []string
	var note string
	cmd := &cobra.Command{
		Use:   "mark <repo>",
		Short: "Add catalog tags or update a repository note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(add) == 0 && len(remove) == 0 && !cmd.Flags().Changed("note") {
				return fmt.Errorf("mark requires --add, --remove, or --note")
			}
			repository, _, err := resolveRepoRef(app, args[0])
			if err != nil {
				return err
			}
			var noteValue *string
			if cmd.Flags().Changed("note") {
				noteValue = &note
			}
			marked, err := patchRepositoryCatalog(ctxOf(), app, repository, add, remove, noteValue)
			if err != nil {
				return fmt.Errorf("mark repository %s: %w", repository.Display(), err)
			}
			fmt.Fprintf(app.Out, "%s\n  path  %s\n  tags  %s\n  note  %s\n",
				repository.Display(), config.Contract(repository.Path),
				dash(strings.Join(marked.Tags, ", ")), dash(marked.Note))
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&add, "add", nil, "tag to add (repeatable)")
	flags.StringArrayVar(&remove, "remove", nil, "tag to remove (repeatable)")
	flags.StringVar(&note, "note", "", "replace the note; pass an empty value to clear it")
	return cmd
}

func patchRepositoryCatalog(ctx context.Context, app *App, repository repo.Repo, add, remove []string, note *string) (*catalog.Entry, error) {
	commonDir := repository.CommonDir
	realPath := repository.RealPath
	if discovered, discoverErr := gitx.Discover(ctx, repository.Path); discoverErr == nil {
		if commonDir == "" {
			commonDir = discovered.GitCommonDir
		}
		if realPath == "" {
			realPath = discovered.Root
		}
	}
	observation := catalog.Observation{
		Host: config.Hostname(), Path: repository.Path, RealPath: realPath,
		CommonDir: commonDir, Name: repository.Name,
		RemoteIdentity: gitx.RemoteFromConfig(commonDir, "origin"),
	}
	add = catalog.NormalizeTags(add)
	remove = catalog.NormalizeTags(remove)
	var marked *catalog.Entry
	err := app.Catalog.WithLock(ctx, func() error {
		entry, ensureErr := app.Registry.EnsureRepository(observation)
		if ensureErr != nil {
			return ensureErr
		}
		marked, ensureErr = app.Registry.Patch(entry.ID, func(candidate *catalog.Entry) error {
			kept := make([]string, 0, len(candidate.Tags)+len(add))
			for _, tag := range candidate.Tags {
				if !slices.Contains(remove, tag) {
					kept = append(kept, tag)
				}
			}
			candidate.Tags = append(kept, add...)
			if note != nil {
				candidate.Note = *note
			}
			return nil
		})
		return ensureErr
	})
	return marked, err
}

type repoListJSONRow struct {
	Name         string               `json:"name"`
	Display      string               `json:"display"`
	Kind         catalog.Kind         `json:"kind"`
	Category     string               `json:"category,omitempty"`
	Path         string               `json:"path"`
	RealPath     string               `json:"real_path,omitempty"`
	Symlink      bool                 `json:"symlink"`
	Bare         bool                 `json:"bare"`
	HasGit       bool                 `json:"has_git"`
	Branch       string               `json:"branch,omitempty"`
	Git          repoListJSONGit      `json:"git"`
	Recovery     repoListJSONRecovery `json:"recovery"`
	Size         *diskusage.Usage     `json:"size"`
	SizeError    string               `json:"size_error,omitempty"`
	LastActivity *string              `json:"last_activity"`
	Worktrees    int                  `json:"worktrees"`
	Notes        repoListJSONNotes    `json:"notes"`
	Asset        *repoListJSONAsset   `json:"asset,omitempty"`
}

type repoListJSONGit struct {
	Dirty     bool `json:"dirty"`
	Changed   int  `json:"changed"`
	Staged    int  `json:"staged"`
	Unstaged  int  `json:"unstaged"`
	Untracked int  `json:"untracked"`
	Ahead     int  `json:"ahead"`
	Behind    int  `json:"behind"`
}

type repoListJSONRecovery struct {
	Remotes           []gitx.RemoteInfo     `json:"remotes"`
	Branches          []gitx.BranchUpstream `json:"branches"`
	LocalOnlyBranches []string              `json:"local_only_branches,omitempty"`
	UpstreamRemotes   []string              `json:"upstream_remotes,omitempty"`
	NoRemote          bool                  `json:"no_remote"`
	MultipleRemotes   bool                  `json:"multiple_remotes"`
	MultipleUpstreams bool                  `json:"multiple_upstreams"`
	Error             string                `json:"error,omitempty"`
}

type repoListJSONNotes struct {
	Count         int     `json:"count"`
	LatestID      string  `json:"latest_id,omitempty"`
	LatestPreview string  `json:"latest_preview,omitempty"`
	LatestUpdated *string `json:"latest_updated,omitempty"`
}

type repoListJSONAsset struct {
	ID    string                  `json:"id"`
	Kind  catalog.Kind            `json:"kind"`
	Phase catalog.ExperimentPhase `json:"phase,omitempty"`
	Tags  []string                `json:"tags"`
	Note  string                  `json:"note"`
}

func makeRepoListJSONRow(row tui.RepoRow) repoListJSONRow {
	kind := catalog.KindRepository
	if row.Asset != nil {
		kind = row.Asset.Kind
	}
	result := repoListJSONRow{
		Name: row.Repo.Name, Display: row.Repo.Display(), Kind: kind,
		Category: row.Repo.Category, Path: row.Repo.Path, RealPath: row.Repo.RealPath,
		Symlink: row.Repo.Symlink, Bare: row.Repo.Bare, HasGit: row.Repo.HasGit,
		Branch: row.Status.Branch,
		Git: repoListJSONGit{
			Dirty: row.Status.Dirty(), Changed: row.Status.Changed,
			Staged: row.Status.Staged, Unstaged: row.Status.Unstaged,
			Untracked: row.Status.Untracked, Ahead: row.Status.Ahead, Behind: row.Status.Behind,
		},
		Recovery: repoListJSONRecovery{
			Remotes:           append([]gitx.RemoteInfo{}, row.Topology.Remotes...),
			Branches:          append([]gitx.BranchUpstream{}, row.Topology.Branches...),
			LocalOnlyBranches: append([]string(nil), row.Topology.LocalOnlyBranches...),
			UpstreamRemotes:   append([]string(nil), row.Topology.UpstreamRemotes...),
			NoRemote:          row.TopologyErr == nil && !row.Topology.HasRemote(),
			MultipleRemotes:   row.Topology.MultipleRemotes(),
			MultipleUpstreams: row.Topology.MultipleUpstreams(),
		},
		LastActivity: rfc3339Value(row.LastActivity), Worktrees: row.Worktrees,
		Notes: repoListJSONNotes{Count: row.NoteCount},
	}
	if row.LatestNote != nil {
		result.Notes.LatestID = row.LatestNote.ID
		result.Notes.LatestPreview = row.LatestNote.Preview(120)
		result.Notes.LatestUpdated = rfc3339Value(row.LatestNote.Updated)
	}
	if row.TopologyErr != nil {
		result.Recovery.Error = row.TopologyErr.Error()
	}
	if row.Usage != nil {
		usage := *row.Usage
		result.Size = &usage
	}
	if row.SizeError != nil {
		result.SizeError = row.SizeError.Error()
	}
	if row.Asset != nil {
		asset := &repoListJSONAsset{
			ID: row.Asset.ID, Kind: row.Asset.Kind,
			Tags: append([]string{}, row.Asset.Tags...), Note: row.Asset.Note,
		}
		if row.Asset.Experiment != nil {
			asset.Phase = row.Asset.Experiment.Phase
		}
		result.Asset = asset
	}
	return result
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

Uses gh, glab or Azure CLI when the host matches one, and plain git otherwise.`,
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
				opened, err := openCheckout(ctx, rt, dest, name)
				if err != nil {
					app.warnf("could not open a session: %v", err)
					return nil
				}
				if rt.Name() == "none" {
					return app.cdDirective(dest)
				}
				return activateRuntime(ctx, rt, opened.Handle)
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
	cmd := &cobra.Command{
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
			opened, err := openCheckout(ctx, rt, r.Path, r.Name)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s  %s", r.Name, config.Contract(r.Path))
			if rt.Name() != "none" {
				fmt.Fprintf(app.Out, "  (%s %s)", rt.Name(), opened.Handle)
			}
			fmt.Fprintln(app.Out)
			if rt.Name() == "none" {
				return app.cdDirective(r.Path)
			}
			return activateRuntime(ctx, rt, opened.Handle)
		},
	}
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
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
				targets, err = repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
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

			t := app.newTable("REPO", "BRANCH", "GIT", "NOTE")
			style := app.outStyle()
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
				noteCell := note
				if note != "" {
					noteCell = style.warning(note)
				}
				t.Add(truncate(r.Name, 28), truncate(st.Branch, 24), style.git(st.Summary()), noteCell)
			}
			t.Render(app.Out)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "every repository under the scan roots")
	cmd.ValidArgsFunction = completeRepos(app)
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
		refresh    bool
		visibility string
		limit      int
	)
	cmd := &cobra.Command{
		Use:     "remote [query]",
		Aliases: []string{"search"},
		Short:   "List and search repositories visible through configured forge CLIs",
		Long: `Combine repositories visible to authenticated gh and glab CLIs with any
configured Azure DevOps organization/project targets, and mark those already cloned
under paths.scan_roots.

The TUI exposes the same data in its REMOTE tab and filters it live with /.
This command is the non-interactive form for scripts, pipes and terminals where
a full-screen UI is not wanted. Fresh complete cache is reused automatically;
use --cached for an offline result or --refresh to force synchronization. The
cache is private, and forge.cache_ttl decides when it needs refresh.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var (
				rows []tui.RemoteRow
				err  error
			)
			if cachedOnly && refresh {
				return fmt.Errorf("--cached cannot be combined with --refresh")
			}
			visibility = strings.ToLower(strings.TrimSpace(visibility))
			switch visibility {
			case "", "public", "private", "internal":
			default:
				return fmt.Errorf("unknown --visibility %q: want public, private or internal", visibility)
			}
			if cachedOnly {
				var ok bool
				var cache forge.Cache
				rows, cache, ok = cachedRemotesAny(ctxOf(), app)
				if !ok {
					return fmt.Errorf("no remote cache; run `dev repo remote --refresh` once online")
				}
				if !cache.Fresh(app.Cfg.Forge.CacheTTL.Duration) {
					app.warnf("remote cache is stale or incomplete; run `dev repo remote --refresh`")
				}
			} else if !refresh {
				var ok bool
				rows, ok = cachedRemotes(ctxOf(), app)
				if !ok {
					rows, err = collectRemotes(ctxOf(), app)
				}
			} else {
				rows, err = collectRemotes(ctxOf(), app)
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
				if visibility != "" && !strings.EqualFold(r.Repo.Visibility, visibility) {
					continue
				}
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
			sort.SliceStable(filtered, func(i, j int) bool {
				if !filtered[i].Repo.UpdatedAt.Equal(filtered[j].Repo.UpdatedAt) {
					return filtered[i].Repo.UpdatedAt.After(filtered[j].Repo.UpdatedAt)
				}
				return filtered[i].Repo.Label() < filtered[j].Repo.Label()
			})
			if limit > 0 && len(filtered) > limit {
				filtered = filtered[:limit]
			}

			if jsonOut {
				enc := json.NewEncoder(app.Out)
				enc.SetIndent("", "  ")
				if encodeErr := enc.Encode(filtered); encodeErr != nil {
					return encodeErr
				}
			} else {
				t := app.newTable("FORGE", "REPOSITORY", "VIS", "LOCAL", "UPDATED", "DESCRIPTION")
				style := app.outStyle()
				for _, r := range filtered {
					local := "—"
					if r.LocalPath != "" {
						local = style.success(config.Contract(r.LocalPath))
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
	f.BoolVar(&cachedOnly, "cached", false, "use the XDG cache without querying forge providers")
	f.BoolVar(&refresh, "refresh", false, "force a complete forge inventory refresh")
	f.StringVar(&visibility, "visibility", "", "filter visibility: public, private or internal")
	f.IntVar(&limit, "limit", 0, "maximum matching repositories to render (0 for all)")
	registerFlagCompletion(cmd, "visibility", fixedCompletions("public", "private", "internal"))
	return cmd
}
