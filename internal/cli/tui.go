package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/stats"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/daviddwlee84/dev-cli/internal/wt"
	"github.com/spf13/cobra"
)

func newTUICmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Interactive dashboard over the inventory",
		Long: `Browse and act on your work in progress.

Shows exactly what "dev ls" shows, from the same code path, plus the ability
to open, park and annotate a task without retyping its name.

Four lists, switched with tab:

  TASKS   change streams dev is tracking — what am I working on
  REPOS   durable repositories under the scan roots — what do I have here
  TRY     scratch experiments and retained lifecycle history
  REMOTE  repositories visible through gh and glab — what can I clone/open

Navigation is vim-style, with arrows alongside:

  j k        move                 ctrl+d ctrl+u   half a page
  g G        top / bottom         h l / tab       previous / next view
  /          filter as you type   esc             clear, then quit

Actions depend on the list:

  TASKS   enter open · p park · c edit next
  REPOS   enter ad hoc · s worktree task · d direct/current-branch task
  TRY     enter open · n create · space lifecycle/metadata actions
  REMOTE  enter open local · c clone after confirmation

  H       selected repo heatmap; b backfills it when empty
  e       edit config; returning live-reloads data, columns, sort and tools
  O / R   cycle / reverse REPOS or TRY sort
  r       reload config + data     1 / 2 / 3  hot / warm / cold
  0       clear filters            a include history  ? help  q quit

External tools are configured, not fixed — see [[tui.tools]] in the config,
and "dev tui tools" for what is bound here. They run in the selected row's checkout;
the dashboard suspends while they do and redraws afterwards, and bindings for
programs that are not installed are not offered.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(app)
		},
	}
	cmd.AddCommand(newTUIToolsCmd(app))
	return cmd
}

func newTUIToolsCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "tools",
		Short: "List the external tool bindings and whether each one works here",
		Long: `Show which programs the dashboard will hand the terminal to.

Bindings come from [[tui.tools]] in the config; with none configured, the
built-in set applies. A configured list replaces the defaults entirely, so
what is bound is always exactly what is written down.

Add your own — an editor, a script, anything you already reach for:

    [[tui.tools]]
    key  = "V"
    name = "nvim"
    run  = "nvim ."

    [[tui.tools]]
    key  = "B"
    name = "vibe"
    run  = "vibe"

The command runs through your shell in the selected row's checkout, so
arguments, environment variables and your own scripts all behave as typed.
Set interactive = true only for aliases/functions from your shell rc; that
binding runs through $SHELL -lic and the mode is shown in this listing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			configured := len(app.Cfg.TUI.Tools) > 0
			source := "built-in defaults"
			if configured {
				source = config.Contract(app.Cfg.Source)
			}
			fmt.Fprintf(app.Out, "tool bindings from %s\n\n", source)

			t := NewTable("KEY", "NAME", "MODE", "RUNS", "AVAILABLE")
			configuredTools := app.Cfg.EffectiveTools()
			for i, tool := range externalTools(app) {
				status := "yes"
				if tool.Available != nil && !tool.Available() {
					status = "no — not on PATH"
				}
				// Command is [shell, -c/-lic, run]; show the run itself.
				run := tool.Command[len(tool.Command)-1]
				mode := "shell"
				if i < len(configuredTools) && configuredTools[i].Interactive {
					mode = "interactive"
				}
				t.Add(tool.Key, tool.Name, mode, run, status)
			}
			t.Render(app.Out)

			if !configured {
				fmt.Fprintln(app.Out, "\nOverride them with [[tui.tools]] in your config; see \"dev tui tools --help\".")
			}
			fmt.Fprintln(app.Err, "\nA tool cannot take a key the dashboard already uses; dev reports the clash on load.")
			return nil
		},
	}
}

// interactive reports whether a real terminal is attached. Piping `dev` into
// anything must produce the plain listing, not terminal control sequences.
func interactive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func runTUI(app *App) error {
	rt := app.Runtime()

	reload := func(ctx context.Context) ([]inventory.Row, error) {
		tasks, err := app.Tasks.List()
		if err != nil {
			return nil, err
		}
		return inventory.Collect(ctx, tasks, rt, inventory.Options{}), nil
	}
	reloadRepos := func(ctx context.Context) ([]tui.RepoRow, error) {
		// Keep Try repositories in the model's local snapshot so cached REMOTE
		// rows can be matched and cleared correctly. The REPOS view filters them.
		return collectReposWithOptions(ctx, app, rt, repoCollectOptions{IncludeTries: true})
	}
	reloadRemote := func(ctx context.Context) ([]tui.RemoteRow, error) {
		return collectRemotes(ctx, app, 200)
	}
	reloadTries := func(ctx context.Context, includeAll bool) ([]tui.TryRow, error) {
		return collectTries(ctx, app, rt, includeAll)
	}

	actions := tui.Actions{
		Reload:       reload,
		ReloadRepos:  reloadRepos,
		ReloadRemote: reloadRemote,
		Repos: tui.RepoActions{
			Patch: func(ctx context.Context, row tui.RepoRow, tags []string, note string) (string, error) {
				remove := []string(nil)
				if row.Asset != nil {
					remove = append(remove, row.Asset.Tags...)
				}
				marked, err := patchRepositoryCatalog(ctx, app, row.Repo, tags, remove, &note)
				if err != nil {
					return "", err
				}
				return "updated metadata for " + marked.Title(), nil
			},
		},
		Tries: tui.TryActions{
			Reload: reloadTries,
			Apply: func(ctx context.Context, request tui.TryRequest) (tui.TryActionResult, error) {
				return applyTryAction(ctx, app, rt, request)
			},
		},
		Sizes: tui.SizeActions{
			Start: func(ctx context.Context, targets []diskusage.Target, force bool) diskusage.Load {
				return app.Sizes.Start(ctx, targets, force)
			},
			Cancel: app.Sizes.Cancel,
		},
		Runtime:     rt,
		Tools:       externalTools(app),
		RepoColumns: app.Cfg.EffectiveRepoColumns(),
		RepoSort:    app.Cfg.EffectiveRepoSort(),
		RepoReverse: app.Cfg.TUI.Repos.Reverse,

		// Open reuses the same paths the commands take, so a cold task
		// selected here is rebuilt rather than reported broken.
		Open: func(ctx context.Context, t *task.Task) (string, error) {
			checkout := checkoutOf(t)
			if _, err := os.Stat(checkout); err != nil {
				return "", fmt.Errorf("%s has no checkout — run `dev resume %s`", t.Title(), t.ID)
			}
			handle, err := openCheckout(ctx, rt, checkout, t.Title())
			if err != nil {
				return "", err
			}
			t.State, t.Owner = task.Hot, config.Hostname()
			if rt.Name() != "none" {
				t.RuntimeHandle = handle
			}
			if err := app.Tasks.Save(t); err != nil {
				return "", err
			}
			annotate(app, rt, t)
			if rt.Name() == "none" {
				return "", nil
			}
			return fmt.Sprintf("%s open in %s (%s)", t.Title(), rt.Name(), handle), nil
		},

		OpenRepo: func(ctx context.Context, r tui.RepoRow) (string, error) {
			handle, err := openCheckout(ctx, rt, r.Repo.Path, r.Repo.Name)
			if err != nil {
				return "", err
			}
			if rt.Name() == "none" {
				return "", nil
			}
			return fmt.Sprintf("%s open in %s (%s)", r.Repo.Name, rt.Name(), handle), nil
		},

		OpenRemote: func(ctx context.Context, r tui.RemoteRow) (string, error) {
			if r.LocalPath == "" {
				return "", fmt.Errorf("%s has no local checkout; press c to clone it", r.Repo.FullName)
			}
			handle, err := openCheckout(ctx, rt, r.LocalPath, r.Repo.Name)
			if err != nil {
				return "", err
			}
			if rt.Name() == "none" {
				return "", nil
			}
			return fmt.Sprintf("%s open in %s (%s)", r.Repo.FullName, rt.Name(), handle), nil
		},

		CloneRemote: func(ctx context.Context, r tui.RemoteRow) (string, string, error) {
			dest := filepath.Join(config.Expand(app.Cfg.Paths.ProjectRoot), r.Repo.Name)
			if _, err := os.Stat(dest); err == nil {
				return "", "", fmt.Errorf("%s already exists; add it to scan_roots or clone somewhere explicit",
					config.Contract(dest))
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return "", "", err
			}
			if _, err := gitx.Run(ctx, filepath.Dir(dest), "clone", r.Repo.CloneURL, dest); err != nil {
				return "", "", err
			}
			handle, err := openCheckout(ctx, rt, dest, r.Repo.Name)
			if err != nil {
				return "", "", fmt.Errorf("cloned to %s, but could not open it: %w", config.Contract(dest), err)
			}
			if rt.Name() == "none" {
				return "cloned " + r.Repo.FullName + " to " + config.Contract(dest), dest, nil
			}
			return fmt.Sprintf("cloned %s to %s; open in %s (%s)",
				r.Repo.FullName, config.Contract(dest), rt.Name(), handle), dest, nil
		},

		Park: func(ctx context.Context, t *task.Task, next string) (string, error) {
			if next != "" {
				t.Next = next
			}
			if t.RuntimeHandle != "" {
				if err := rt.Close(ctx, t.RuntimeHandle); err != nil {
					return "", err
				}
				t.RuntimeHandle = ""
			}
			// The dashboard only parks warm: going cold removes a checkout,
			// which is too consequential for a single keystroke.
			t.State, t.Owner = task.Warm, config.Hostname()
			if err := app.Tasks.Save(t); err != nil {
				return "", err
			}
			return t.Title() + " parked warm — worktree and branch kept", nil
		},

		SetNext: func(ctx context.Context, t *task.Task, next string) error {
			t.Next = next
			if err := app.Tasks.Save(t); err != nil {
				return err
			}
			annotate(app, rt, t)
			return nil
		},

		// Start is the bridge from "here are my repositories" to "here is what
		// I am working on" — the step that otherwise means dropping out of the
		// dashboard to type a command.
		Start: func(ctx context.Context, r tui.RepoRow, name string) (string, error) {
			branch := "feat/" + config.Slug(name)
			base := gitx.DefaultBranch(ctx, r.Repo.Path)
			id := task.MakeID(r.Repo.Name, branch)
			if _, err := app.Tasks.Get(id); err == nil {
				return "", fmt.Errorf("%s already exists", id)
			}

			m := &wt.Manager{Cfg: app.Cfg, Runtime: rt}
			res, err := m.Create(ctx, wt.CreateRequest{
				RepoPath: r.Repo.Path, RepoName: r.Repo.Name, Branch: branch,
				Base: base, Category: r.Repo.Category, Label: name,
			})
			if err != nil {
				return "", err
			}
			t := &task.Task{
				Name: name, Repo: r.Repo.Name, RepoPath: r.Repo.Path,
				Branch: branch, Base: base, WorktreePath: res.Path, Mode: task.ModeWorktree,
				State: task.Hot, Owner: config.Hostname(), RuntimeHandle: res.RuntimeHandle,
			}
			if err := app.Tasks.Save(t); err != nil {
				return "", err
			}
			annotate(app, rt, t)
			return fmt.Sprintf("started %s on %s", name, branch), nil
		},

		StartDirect: func(ctx context.Context, r tui.RepoRow, name string) (string, error) {
			st, err := gitx.StatusOf(ctx, r.Repo.Path)
			if err != nil {
				return "", err
			}
			if st.Detached || st.Branch == "" {
				return "", fmt.Errorf("direct task needs a named branch; this repo has detached HEAD")
			}
			id := task.MakeID(r.Repo.Name, st.Branch)
			if existing, err := app.Tasks.Get(id); err == nil && existing.State != task.Done {
				return "", fmt.Errorf("task %s already exists (state %s)", existing.ID, existing.State)
			}
			handle, err := rt.Open(ctx, r.Repo.Path, r.Repo.Name+"/"+name)
			if err != nil {
				return "", err
			}
			t := &task.Task{
				Name: name, Repo: r.Repo.Name, RepoPath: r.Repo.Path,
				Branch: st.Branch, Base: st.Branch, Mode: task.ModeDirect,
				State: task.Hot, Owner: config.Hostname(),
			}
			if rt.Name() != "none" {
				t.RuntimeHandle = handle
			}
			if err := app.Tasks.Save(t); err != nil {
				return "", err
			}
			annotate(app, rt, t)
			return fmt.Sprintf("tracking %s directly on %s; no branch/worktree created", name, st.Branch), nil
		},

		LoadStats: func(ctx context.Context, repoName string) (tui.StatsPanel, error) {
			store, err := stats.Open(stats.Path(app.Cfg.StateDir()))
			if err != nil {
				return tui.StatsPanel{}, err
			}
			defer store.Close()
			until := time.Now()
			since := until.AddDate(-1, 0, 0)
			totals, err := store.DayTotals(stats.Query{
				Since: since, Until: until, Repo: repoName, ExactRepo: true,
			})
			if err != nil {
				return tui.StatsPanel{}, err
			}
			seconds, days := 0, 0
			for _, value := range totals {
				seconds += value
				if value > 0 {
					days++
				}
			}
			return tui.StatsPanel{
				Repo: repoName, Seconds: seconds, ActiveDays: days,
				Since: since, Until: until,
				Heatmap: stats.Heatmap(totals, stats.HeatmapOptions{
					Since: since, Until: until, Legend: true, WeekdayLabels: true,
				}),
			}, nil
		},

		BackfillStats: func(ctx context.Context, repoName string) error {
			r, _, err := repo.Resolve(ctx, app.Cfg.ScanRoots(), repoName)
			if err != nil {
				return err
			}
			store, err := stats.Open(stats.Path(app.Cfg.StateDir()))
			if err != nil {
				return err
			}
			defer store.Close()
			_, err = stats.BackfillGit(ctx, store, []repo.Repo{r}, time.Now().AddDate(-1, 0, 0), "")
			return err
		},

		EditConfig: func() (*exec.Cmd, error) {
			proc, _, _, err := configEditorProcess(app, "")
			return proc, err
		},

		ReloadConfig: func(ctx context.Context) (tui.ConfigUpdate, string, error) {
			oldRuntime := rt.Name()
			if err := app.Load(); err != nil {
				return tui.ConfigUpdate{}, "", err
			}
			status := "config + data reloaded"
			if nextRuntime := app.Runtime().Name(); nextRuntime != oldRuntime {
				status += fmt.Sprintf("; restart TUI to switch runtime %s → %s", oldRuntime, nextRuntime)
			}
			return tui.ConfigUpdate{
				Tools:       externalTools(app),
				RepoColumns: app.Cfg.EffectiveRepoColumns(),
				RepoSort:    app.Cfg.EffectiveRepoSort(),
				RepoReverse: app.Cfg.TUI.Repos.Reverse,
			}, status, nil
		},
	}

	// Enter the alternate screen immediately. Local inventory is loaded by
	// Init in the background rather than making the terminal appear frozen
	// while dozens of repos are probed.
	model := tui.New(actions, nil, nil).BeginLoading()
	if cached, ok := cachedRemoteRows(app); ok {
		model = model.WithRemotes(cached)
	}
	final, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	// A directory choice can only be honoured once the alternate screen is
	// torn down, and only by the shell wrapper.
	if m, ok := final.(tui.Model); ok {
		if dir := m.Chosen(); dir != "" {
			return app.cdDirective(dir)
		}
	}
	return nil
}

func collectTries(ctx context.Context, app *App, rt runtime.Runtime, includeAll bool) ([]tui.TryRow, error) {
	service, err := newExperimentService(app)
	if err != nil {
		return nil, err
	}
	items, diagnostics, listErr := service.List(ctx, experiment.ListOptions{All: includeAll})
	warnExperimentDiagnostics(app, diagnostics)
	sessions, _ := rt.List(ctx)
	rows := make([]tui.TryRow, 0, len(items))
	for _, item := range items {
		row := tui.TryRow{Item: item}
		if item.Entry != nil {
			if location, ok := item.Entry.LocationFor(config.Hostname()); ok {
				copy := location
				row.Location = &copy
			}
		}
		sizeRepo := item.Live.Repo
		if sizeRepo == nil && item.Live.CurrentPath != "" {
			if discovered, discoverErr := gitx.Discover(ctx, item.Live.CurrentPath); discoverErr == nil {
				sizeRepo = &discovered
			}
		}
		if sizeRepo != nil {
			worktreeCount := 0
			if worktrees, worktreeErr := gitx.Worktrees(ctx, item.Live.CurrentPath); worktreeErr == nil && len(worktrees) > 1 {
				worktreeCount = len(worktrees) - 1
			}
			row.SizeTarget = diskusage.FromGit(*sizeRepo, worktreeCount)
		} else if item.Live.CurrentPath != "" {
			row.SizeTarget = diskusage.Plain(item.Live.CurrentPath)
		}
		if item.Live.Present && item.Live.Repo != nil {
			row.Topology, row.TopologyErr = gitx.RecoveryTopologyOf(ctx, item.Live.CurrentPath)
		}
		if item.Live.Present {
			for _, session := range sessions {
				if session.Covers(item.Live.CurrentPath) ||
					(item.Live.RealPath != "" && session.Covers(item.Live.RealPath)) {
					row.Live = true
					row.Runtime, row.RuntimeHandle, row.RuntimeStatus = rt.Name(), session.Handle, session.AgentStatus
					break
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, listErr
}

func applyTryAction(ctx context.Context, app *App, rt runtime.Runtime, request tui.TryRequest) (tui.TryActionResult, error) {
	service, err := newExperimentService(app)
	if err != nil {
		return tui.TryActionResult{}, err
	}
	resolve := func() (experiment.Item, error) {
		item, diagnostics, resolveErr := service.ResolveWithOptions(ctx, request.ID, experiment.ResolveOptions{
			IncludeDeprecated: true, IncludeArchived: true,
			IncludeEvicted: true, IncludeGraduated: true,
		})
		warnExperimentDiagnostics(app, diagnostics)
		return item, resolveErr
	}
	open := func(item experiment.Item, status string) (tui.TryActionResult, error) {
		if !item.Live.Present || item.Live.CurrentPath == "" {
			return tui.TryActionResult{}, fmt.Errorf("Try %s is not present on this host", item.DisplayName())
		}
		handle, openErr := openCheckout(ctx, rt, item.Live.CurrentPath, item.DisplayName())
		if openErr != nil {
			return tui.TryActionResult{}, openErr
		}
		result := tui.TryActionResult{Status: status, RefreshRepos: true}
		if rt.Name() == "none" {
			result.CD = item.Live.CurrentPath
		} else {
			result.Status += fmt.Sprintf(" in %s (%s)", rt.Name(), handle)
		}
		if _, touchErr := service.Touch(ctx, item.ID); touchErr != nil {
			return result, fmt.Errorf("%s, but could not record activity: %w", status, touchErr)
		}
		return result, nil
	}

	switch request.Action {
	case tui.TryOpen:
		item, resolveErr := resolve()
		if resolveErr != nil {
			return tui.TryActionResult{}, resolveErr
		}
		return open(item, "opened "+item.DisplayName())

	case tui.TryCreate:
		created, createErr := service.ResolveOrCreate(ctx, experiment.CreateRequest{
			Name: request.Name, Clone: request.Clone, NoGit: request.NoGit,
		})
		warnExperimentDiagnostics(app, created.Diagnostics)
		if created.InitWarning != nil {
			app.warnf("could not git init: %v", created.InitWarning)
		}
		if createErr != nil {
			return tui.TryActionResult{}, createErr
		}
		status := "created " + created.Item.DisplayName()
		if created.Existing {
			status = "opened existing " + created.Item.DisplayName()
		}
		result, openErr := open(created.Item, status)
		result.RefreshRepos = true
		return result, openErr

	case tui.TryMark:
		item, resolveErr := resolve()
		if resolveErr != nil {
			return tui.TryActionResult{}, resolveErr
		}
		desired := catalog.NormalizeTags(request.Tags)
		remove := append([]string(nil), item.Tags...)
		note := request.Note
		updated, diagnostics, patchErr := service.Patch(ctx, experiment.PatchRequest{
			Ref: item.ID, AddTags: desired, RemoveTags: remove, Note: &note,
		})
		warnExperimentDiagnostics(app, diagnostics)
		return tui.TryActionResult{
			Status: "updated metadata for " + updated.DisplayName(), RefreshRepos: true,
		}, patchErr

	case tui.TryDeprecate:
		transition, transitionErr := service.Deprecate(ctx, experiment.TransitionRequest{Ref: request.ID})
		warnExperimentDiagnostics(app, transition.Diagnostics)
		return tui.TryActionResult{Status: "deprecated " + transition.Item.DisplayName()}, transitionErr

	case tui.TryReactivate:
		transition, transitionErr := service.Reactivate(ctx, experiment.TransitionRequest{Ref: request.ID})
		warnExperimentDiagnostics(app, transition.Diagnostics)
		return tui.TryActionResult{Status: "reactivated " + transition.Item.DisplayName()}, transitionErr

	case tui.TryArchive:
		transition, transitionErr := service.Archive(ctx, experiment.TransitionRequest{Ref: request.ID})
		warnExperimentDiagnostics(app, transition.Diagnostics)
		return tui.TryActionResult{
			Status: "archived " + transition.Item.DisplayName(), RefreshRepos: true,
		}, transitionErr

	case tui.TryRestore:
		transition, transitionErr := service.Restore(ctx, experiment.TransitionRequest{Ref: request.ID, To: request.To})
		warnExperimentDiagnostics(app, transition.Diagnostics)
		return tui.TryActionResult{
			Status: "restored " + transition.Item.DisplayName(), RefreshRepos: true,
		}, transitionErr

	case tui.TryGraduate:
		graduated, graduateErr := service.Graduate(ctx, experiment.GraduateRequest{
			Ref: request.ID, Category: request.Category, Name: request.Name,
		})
		warnExperimentDiagnostics(app, graduated.Diagnostics)
		if graduateErr != nil {
			return tui.TryActionResult{}, graduateErr
		}
		result, openErr := open(graduated.Item, "graduated "+graduated.Plan.Name)
		result.RefreshRepos = true
		return result, openErr
	}
	return tui.TryActionResult{}, fmt.Errorf("unknown Try action %q", request.Action)
}

// repoCollectOptions leaves an opt-in path for the future TRY view while the
// ordinary repository inventory suppresses active and deprecated Tries.
type repoCollectOptions struct {
	IncludeTries bool
}

// collectRepos builds the default repository view: what exists, plus how much
// of it is in flight, excluding cataloged active/deprecated Tries.
func collectRepos(ctx context.Context, app *App, rt runtime.Runtime) ([]tui.RepoRow, error) {
	return collectReposWithOptions(ctx, app, rt, repoCollectOptions{})
}

func collectReposWithOptions(ctx context.Context, app *App, rt runtime.Runtime, options repoCollectOptions) ([]tui.RepoRow, error) {
	repos, err := repo.Discover(ctx, app.Cfg.ScanRoots(), repo.DefaultOptions())
	if err != nil {
		return nil, err
	}
	assets, catalogComplete, err := joinRepoAssets(app, repos)
	if err != nil {
		return nil, err
	}
	if !options.IncludeTries {
		visibleRepos := make([]repo.Repo, 0, len(repos))
		visibleAssets := make([]*catalog.Entry, 0, len(repos))
		for index, repository := range repos {
			if suppressTryRepo(assets[index], catalogComplete) {
				continue
			}
			visibleRepos = append(visibleRepos, repository)
			visibleAssets = append(visibleAssets, assets[index])
		}
		repos, assets = visibleRepos, visibleAssets
	}
	tasks, err := app.Tasks.List()
	if err != nil {
		return nil, err
	}
	byRepo := map[string][]*task.Task{}
	for _, t := range tasks {
		byRepo[t.RepoPath] = append(byRepo[t.RepoPath], t)
	}
	sessions, _ := rt.List(ctx)

	out := make([]tui.RepoRow, len(repos))
	// Each repo needs status, worktree and remote subprocesses. Serialising
	// ~3*56 git processes was the measured 4.2s startup bottleneck; eight
	// workers brings that down without forking hundreds at once.
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, r := range repos {
		wg.Add(1)
		go func(i int, r repo.Repo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			row := tui.RepoRow{Repo: r, Asset: assets[i]}
			if r.HasGit {
				row.Topology, row.TopologyErr = gitx.RecoveryTopologyOf(ctx, r.Path)
			}
			row.Tasks = append(row.Tasks, byRepo[r.Path]...)
			if r.RealPath != "" && r.RealPath != r.Path {
				row.Tasks = append(row.Tasks, byRepo[r.RealPath]...)
			}
			if !r.Bare {
				row.Status, _ = gitx.StatusOf(ctx, r.Path)
				if row.Status.LatestChange.After(row.LastActivity) {
					row.LastActivity = row.Status.LatestChange
				}
				// A normal clone stores one administrative directory per
				// linked worktree. Reading it avoids spawning another git
				// process for almost every repo in the main inventory.
				if entries, err := os.ReadDir(filepath.Join(r.CommonDir, "worktrees")); err == nil {
					for _, entry := range entries {
						if entry.IsDir() {
							row.Worktrees++
						}
					}
				}
				row.RemoteForge, row.RemoteName = forge.IdentityFromURL(gitx.RemoteFromConfig(r.CommonDir, "origin"))
				if unix, _, err := gitx.LastCommit(ctx, r.Path); err == nil && unix > 0 {
					commitTime := time.Unix(unix, 0)
					if commitTime.After(row.LastActivity) {
						row.LastActivity = commitTime
					}
				}
			}
			for _, tracked := range row.Tasks {
				if tracked.Updated.After(row.LastActivity) {
					row.LastActivity = tracked.Updated
				}
			}
			for _, session := range sessions {
				if session.Covers(r.Path) || (r.RealPath != "" && session.Covers(r.RealPath)) {
					row.Live = true
					row.Runtime, row.RuntimeHandle, row.RuntimeStatus =
						rt.Name(), session.Handle, session.AgentStatus
					break
				}
			}
			if r.HasGit {
				row.SizeTarget = diskusage.FromGit(gitx.Repo{
					Root: r.Path, GitDir: r.GitDir, GitCommonDir: r.CommonDir,
					MainRoot: r.MainRoot, Name: r.Name, Bare: r.Bare,
				}, row.Worktrees)
			} else {
				row.SizeTarget = diskusage.Plain(r.Path)
			}
			out[i] = row
		}(i, r)
	}
	wg.Wait()
	return out, ctx.Err()
}

// externalTools resolves the configured tool bindings.
//
// Configurable rather than fixed because which program you reach for is
// personal, and because the useful ones are often your own scripts and
// aliases, not just the well-known tools.
func externalTools(app *App) []tui.Tool {
	var out []tui.Tool
	for _, t := range app.Cfg.EffectiveTools() {
		run := t.Run
		name := t.Name
		if name == "" {
			name = firstWord(run)
		}
		command := []string{shellPath(), "-c", run}
		if t.Interactive {
			// The command string passed to shell -lic is parsed before some
			// shells finish loading aliases. Parse only eval "$1" up front,
			// then evaluate the configured command after rc loading, so both
			// aliases (vibe) and functions (claude-plans-here) work.
			command = []string{shellPath(), "-lic", `eval "$1"`, "dev-tui-tool", run}
		}
		out = append(out, tui.Tool{
			Key: t.Key, Name: name, Command: command,
			Available: commandRunnable(run, t.Interactive),
		})
	}
	return out
}

// commandRunnable resolves the command's first word on PATH, expanding a
// leading environment variable so "$EDITOR ." checks the editor itself.
func commandRunnable(run string, interactive bool) func() bool {
	// Availability is stable for the lifetime of one dashboard. In particular,
	// probing an interactive alias starts a login shell; doing that on every
	// render would turn a 60fps UI into a shell-launch benchmark.
	var once sync.Once
	available := false
	return func() bool {
		once.Do(func() { available = checkCommandRunnable(run, interactive) })
		return available
	}
}

func checkCommandRunnable(run string, interactive bool) bool {
	word := firstWord(run)
	if strings.HasPrefix(word, "$") {
		word = os.Getenv(strings.TrimPrefix(word, "$"))
		if word == "" {
			return false
		}
		word = firstWord(word)
	}
	if word == "" {
		return false
	}
	if interactive {
		// Ask the configured shell after it loads its rc files; LookPath
		// cannot see aliases or functions.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		probe := exec.CommandContext(ctx, shellPath(), "-lic",
			`command -v "$1" >/dev/null 2>&1`, "dev-tool-probe", word)
		return probe.Run() == nil
	}
	if filepath.IsAbs(word) {
		_, err := os.Stat(word)
		return err == nil
	}
	_, err := exec.LookPath(word)
	return err == nil
}

func firstWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func shellPath() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}

// remoteCachePath is private because the inventory can contain names and URLs
// of private repositories.
func remoteCachePath() string {
	return filepath.Join(config.CacheHome(), "dev", "remotes.json")
}

// cachedRemoteRows reads only the private JSON cache — no repository scan or
// subprocess — so it is safe on the startup path. Local clone markers are
// filled when the background local inventory arrives.
func cachedRemoteRows(app *App) ([]tui.RemoteRow, bool) {
	cache, ok := forge.LoadCache(remoteCachePath(), app.Cfg.Forge.CacheTTL.Duration)
	if !ok {
		return nil, false
	}
	out := make([]tui.RemoteRow, 0, len(cache.Repos))
	for _, rr := range cache.Repos {
		out = append(out, tui.RemoteRow{Repo: rr})
	}
	applyCatalogRemoteMatches(app, out)
	return out, true
}

// cachedRemotesForRepoRows uses the local repo data the dashboard already
// collected, avoiding another process-spawning scan on startup.
func cachedRemotesForRepoRows(app *App, locals []tui.RepoRow) ([]tui.RemoteRow, bool) {
	cache, ok := forge.LoadCache(remoteCachePath(), app.Cfg.Forge.CacheTTL.Duration)
	if !ok {
		return nil, false
	}
	byRemote := map[string]tui.RepoRow{}
	ambiguous := map[string]bool{}
	for _, r := range locals {
		if r.RemoteForge == forge.Unknown || r.RemoteName == "" {
			continue
		}
		key := string(r.RemoteForge) + "/" + strings.ToLower(r.RemoteName)
		if ambiguous[key] {
			continue
		}
		if _, exists := byRemote[key]; exists {
			delete(byRemote, key)
			ambiguous[key] = true
			continue
		}
		byRemote[key] = r
	}
	out := make([]tui.RemoteRow, 0, len(cache.Repos))
	for _, rr := range cache.Repos {
		row := tui.RemoteRow{Repo: rr}
		if local, ok := byRemote[string(rr.Forge)+"/"+strings.ToLower(rr.FullName)]; ok {
			row.LocalPath, row.LocalName = local.Repo.Path, local.Repo.Display()
			row.LocalKind = catalog.KindRepository
			if local.Asset != nil {
				row.LocalKind = local.Asset.Kind
			}
		}
		out = append(out, row)
	}
	applyCatalogRemoteMatches(app, out)
	return out, true
}

// cachedRemotes returns a fresh, already-local-matched cache for instant TUI
// navigation. Local paths are recomputed rather than cached because clones can
// move independently of the forge inventory.
func cachedRemotes(ctx context.Context, app *App) ([]tui.RemoteRow, bool) {
	cache, ok := forge.LoadCache(remoteCachePath(), app.Cfg.Forge.CacheTTL.Duration)
	if !ok {
		return nil, false
	}
	return matchRemoteLocals(ctx, app, cache.Repos), true
}

// collectRemotes aggregates gh and glab, caches the normalised response, then
// marks remotes that already have a checkout under the configured scan roots.
// Calls run concurrently so one slow forge does not serialise the other.
func collectRemotes(ctx context.Context, app *App, limit int) ([]tui.RemoteRow, error) {
	if limit <= 0 {
		limit = app.Cfg.Forge.RemoteLimit
	}
	type result struct {
		repos []forge.RemoteRepo
		err   error
	}
	ch := make(chan result, len(forge.All()))
	var wg sync.WaitGroup
	available := 0
	for _, f := range forge.All() {
		if !f.Available() {
			continue
		}
		available++
		wg.Add(1)
		go func(f forge.Forge) {
			defer wg.Done()
			r, err := f.ListRepos(ctx, limit)
			ch <- result{repos: r, err: err}
		}(f)
	}
	wg.Wait()
	close(ch)
	if available == 0 {
		return nil, fmt.Errorf("neither gh nor glab is installed")
	}

	var (
		remoteRepos []forge.RemoteRepo
		errs        []error
	)
	for res := range ch {
		if res.err != nil {
			errs = append(errs, res.err)
		}
		remoteRepos = append(remoteRepos, res.repos...)
	}
	// Cache partial results too. They are exactly what was observed, and a
	// partial list is more useful on the next switch than another six-second
	// wait merely to rediscover the same provider failure.
	if len(remoteRepos) > 0 && limit >= app.Cfg.Forge.RemoteLimit {
		_ = forge.SaveCache(remoteCachePath(), remoteRepos)
	}
	return matchRemoteLocals(ctx, app, remoteRepos), errors.Join(errs...)
}

func matchRemoteLocals(ctx context.Context, app *App, remoteRepos []forge.RemoteRepo) []tui.RemoteRow {
	locals, _ := repo.Discover(ctx, app.Cfg.ScanRoots(), repo.DefaultOptions())
	assets, _, assetErr := joinRepoAssets(app, locals)
	if assetErr != nil {
		app.warnf("could not join catalog metadata to remote locals: %v", assetErr)
		assets = make([]*catalog.Entry, len(locals))
	}
	type localMatch struct {
		repository repo.Repo
		asset      *catalog.Entry
	}
	localByRemote := map[string]localMatch{}
	ambiguous := map[string]bool{}
	for index, r := range locals {
		if r.Bare {
			continue
		}
		kind, name := forge.IdentityFromURL(gitx.RemoteFromConfig(r.CommonDir, "origin"))
		if kind == forge.Unknown || name == "" {
			continue
		}
		key := string(kind) + "/" + strings.ToLower(name)
		if ambiguous[key] {
			continue
		}
		if _, exists := localByRemote[key]; exists {
			delete(localByRemote, key)
			ambiguous[key] = true
			continue
		}
		localByRemote[key] = localMatch{repository: r, asset: assets[index]}
	}
	out := make([]tui.RemoteRow, 0, len(remoteRepos))
	for _, rr := range remoteRepos {
		row := tui.RemoteRow{Repo: rr}
		if local, ok := localByRemote[string(rr.Forge)+"/"+strings.ToLower(rr.FullName)]; ok {
			row.LocalPath, row.LocalName = local.repository.Path, local.repository.Display()
			row.LocalKind = catalog.KindRepository
			if local.asset != nil {
				row.LocalKind = local.asset.Kind
			}
		}
		out = append(out, row)
	}
	// Linked-worktree Tries are intentionally absent from repo.Discover's project
	// inventory, so use their explicit catalog identity as a second safe source.
	applyCatalogRemoteMatches(app, out)
	return out
}

func applyCatalogRemoteMatches(app *App, rows []tui.RemoteRow) {
	if app == nil || app.Catalog == nil || len(rows) == 0 {
		return
	}
	entries, diagnostics, err := app.Catalog.ListWithDiagnostics()
	if err != nil {
		app.warnf("could not read catalog for remote matching: %v", err)
		return
	}
	for _, diagnostic := range diagnostics {
		app.warnf("%s", diagnostic.Error())
	}
	if len(diagnostics) > 0 {
		return
	}
	host := config.Hostname()
	byIdentity := make(map[string][]*catalog.Entry)
	for _, entry := range entries {
		location, ok := entry.LocationFor(host)
		if !ok || location.State != catalog.LocationPresent || entry.RemoteIdentity == "" ||
			liveCatalogLocationPath(location) == "" {
			continue
		}
		byIdentity[entry.RemoteIdentity] = append(byIdentity[entry.RemoteIdentity], entry)
	}
	for index := range rows {
		identity := catalog.NormalizeRemoteIdentity(rows[index].Repo.CloneURL)
		matches := byIdentity[identity]
		if identity == "" || len(matches) != 1 {
			continue
		}
		location, _ := matches[0].LocationFor(host)
		rows[index].LocalPath = liveCatalogLocationPath(location)
		rows[index].LocalName = matches[0].Name
		rows[index].LocalKind = matches[0].Kind
	}
}

func liveCatalogLocationPath(location catalog.Location) string {
	for _, candidate := range []string{location.CurrentPath, location.RealPath} {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}
