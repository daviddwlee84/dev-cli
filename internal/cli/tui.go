package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/note"
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

Six lists, switched with tab:

  TASKS   change streams dev is tracking — what am I working on
  REPOS   durable repositories under the scan roots — what do I have here
  FLEET   repositories and active work across configured SSH machines
  TRY     scratch experiments and retained lifecycle history
  REMOTE  repositories visible through configured forge CLIs — what can I clone/open
  SKILLS  project/global agent skills, agents, sources and update state

Navigation is vim-style, with arrows alongside:

  j k        move                 ctrl+d ctrl+u   half a page
  g G        top / bottom         h l / tab       previous / next view
  /          filter as you type   esc             clear, then quit

Actions depend on the list:

  TASKS   enter open · p park · c edit next
	  REPOS   enter ad hoc · space worktrees · m metadata · s worktree task · d direct task
	  FLEET   enter Herdr/SSH open · Git changes are read-only here
	  TRY     enter open · n create · space lifecycle/metadata actions
  REMOTE  enter open local · c clone after confirmation
  SKILLS  a interactive add · c check updates · u update selected after confirmation

  y       copy menu: yy context · yp path · yb branch · ys sessions · yw WT paths
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

			t := app.newTable("KEY", "NAME", "MODE", "RUNS", "AVAILABLE")
			style := app.outStyle()
			configuredTools := app.Cfg.EffectiveTools()
			for i, tool := range externalTools(app) {
				status := "yes"
				if tool.Available != nil && !tool.Available() {
					status = style.warning("no — not on PATH")
				} else {
					status = style.success(status)
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

func tuiStartRequest(r tui.RepoRow, branch, base string) wt.CreateRequest {
	return wt.CreateRequest{
		RepoPath: r.Repo.Path, RepoName: r.Repo.Name, Branch: branch,
		Base: base, Category: r.Repo.Category,
		Label: worktreeRuntimeLabel(r.Repo.Name, branch),
	}
}

func tuiStartedTask(r tui.RepoRow, name, branch, base string, res *wt.CreateResult, rt runtime.Runtime) *task.Task {
	t := &task.Task{
		Name: name, Repo: r.Repo.Name, RepoPath: r.Repo.Path,
		Branch: branch, Base: base, WorktreePath: res.Path, Mode: task.ModeWorktree,
		State: task.Hot, Owner: config.Hostname(),
	}
	setTaskRuntime(t, rt, res.Runtime)
	return t
}

func runTUI(app *App) error {
	// --color, NO_COLOR and TERM=dumb governed every other surface but stopped
	// at the dashboard, which resolved its own palette independently.
	tui.SetColorEnabled(app.outStyle().enabled)
	rt := app.Runtime()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	projectRoot := agentskill.ProjectRoot(ctxOf(), cwd)

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
		return collectRemotes(ctx, app)
	}
	reloadFleet := func(ctx context.Context) ([]tui.FleetRow, error) {
		results, _, err := collectFleet(ctx, app, fleetCollectOptions{})
		return fleetRows(results), err
	}
	reloadSkills := func(ctx context.Context) ([]agentskill.Skill, error) {
		return agentskill.List(ctx, projectRoot, agentskill.ListOptions{})
	}
	reloadTries := func(ctx context.Context, includeAll bool) ([]tui.TryRow, error) {
		return collectTries(ctx, app, rt, includeAll)
	}

	actions := tui.Actions{
		Reload:       reload,
		ReloadRepos:  reloadRepos,
		ReloadRemote: reloadRemote,
		ReloadFleet:  reloadFleet,
		ReloadSkills: reloadSkills,
		CheckSkills: func(ctx context.Context, rows []agentskill.Skill) []agentskill.Skill {
			return agentskill.CheckUpdates(ctx, rows)
		},
		AddSkill: func() (*exec.Cmd, error) {
			return agentskill.AddCommand(context.Background(), projectRoot, agentskill.DefaultSource)
		},
		UpdateSkill: func(row agentskill.Skill) (*exec.Cmd, error) {
			return agentskill.UpdateCommand(context.Background(), projectRoot, row.Name, row.Scope)
		},
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
		Notes: tui.NoteActions{
			List: func(ctx context.Context, target tui.NoteTarget) ([]*note.Note, error) {
				entry, _, err := ensureNoteRepository(ctx, app, target)
				if err != nil {
					return nil, err
				}
				return app.Notes.List(entry.ID)
			},
			Search: func(ctx context.Context, target tui.NoteTarget, query string) ([]*note.Note, error) {
				entry, _, err := ensureNoteRepository(ctx, app, target)
				if err != nil {
					return nil, err
				}
				return app.Notes.Search(query, entry.ID, 100)
			},
			Add: func(ctx context.Context, target tui.NoteTarget, body string) (string, error) {
				entry, _, err := ensureNoteRepository(ctx, app, target)
				if err != nil {
					return "", err
				}
				n, err := app.Notes.Add(ctx, entry.ID, entry.Title(), body, nil)
				if err != nil {
					return "", err
				}
				return "added note " + n.ID[:8] + " to " + entry.Title(), nil
			},
			Delete: func(ctx context.Context, n *note.Note) (string, error) {
				if err := app.Notes.Delete(ctx, n.ID); err != nil {
					return "", err
				}
				return "deleted note " + n.ID[:8], nil
			},
			Edit: func(n *note.Note) (tui.NoteEdit, error) {
				return prepareTUINoteEdit(app, n)
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
		Copy:        clipboard.WriteAll,

		// Open is navigation-only. The model rejects missing/unregistered and
		// cold worktree tasks before this callback so reconciliation and writer
		// ownership remain explicit `dev sweep` / `dev resume` operations.
		Open: func(ctx context.Context, t *task.Task) (tui.OpenResult, error) {
			checkout := checkoutOf(t)
			if _, err := os.Stat(checkout); err != nil {
				return tui.OpenResult{}, fmt.Errorf("%s has no checkout — run `dev resume %s`", t.Title(), t.ID)
			}
			handle, err := openCheckout(ctx, rt, checkout, t.Title())
			if err != nil {
				return tui.OpenResult{}, err
			}
			// Enter is navigation only. Claiming a writer is an explicit
			// `dev resume`/start action with collision checks.
			if rt.Name() == "none" {
				return tui.OpenResult{}, nil
			}
			return tui.OpenResult{Status: fmt.Sprintf("%s open in %s (%s)", t.Title(), rt.Name(), handle.Handle), RuntimeHandle: handle.Handle}, nil
		},

		OpenRepo: func(ctx context.Context, r tui.RepoRow) (tui.OpenResult, error) {
			handle, err := openCheckout(ctx, rt, r.Repo.Path, r.Repo.Name)
			if err != nil {
				return tui.OpenResult{}, err
			}
			if rt.Name() == "none" {
				return tui.OpenResult{}, nil
			}
			return tui.OpenResult{Status: fmt.Sprintf("%s open in %s (%s)", r.Repo.Name, rt.Name(), handle.Handle), RuntimeHandle: handle.Handle}, nil
		},

		OpenCheckout: func(ctx context.Context, r tui.RepoRow, checkout inventory.RepoCheckout) (tui.OpenResult, error) {
			branch := checkout.Branch()
			if branch == "" {
				branch = filepath.Base(checkout.Worktree.Path)
			}
			label := r.Repo.Name + "/" + branch
			handle, err := openCheckout(ctx, rt, checkout.Worktree.Path, label)
			if err != nil {
				return tui.OpenResult{}, err
			}
			if rt.Name() == "none" {
				return tui.OpenResult{}, nil
			}
			return tui.OpenResult{Status: fmt.Sprintf("%s open in %s (%s)", label, rt.Name(), handle.Handle), RuntimeHandle: handle.Handle}, nil
		},

		OpenRemote: func(ctx context.Context, r tui.RemoteRow) (tui.OpenResult, error) {
			if r.LocalPath == "" {
				return tui.OpenResult{}, fmt.Errorf("%s has no local checkout; press c to clone it", r.Repo.FullName)
			}
			handle, err := openCheckout(ctx, rt, r.LocalPath, r.Repo.Name)
			if err != nil {
				return tui.OpenResult{}, err
			}
			if rt.Name() == "none" {
				return tui.OpenResult{}, nil
			}
			return tui.OpenResult{Status: fmt.Sprintf("%s open in %s (%s)", r.Repo.FullName, rt.Name(), handle.Handle), RuntimeHandle: handle.Handle}, nil
		},

		OpenFleet: func(ctx context.Context, row tui.FleetRow) (*exec.Cmd, error) {
			if row.Repository == nil {
				return nil, fmt.Errorf("host %s has no repository selected", row.Host)
			}
			executable, err := os.Executable()
			if err != nil {
				return nil, err
			}
			args := []string{}
			if app.configPath != "" {
				args = append(args, "--config", app.configPath)
			}
			if app.remotesPath != "" {
				args = append(args, "--remotes", app.remotesPath)
			}
			if row.Local {
				args = append(args, "repo", "open", row.Repository.Path)
			} else {
				args = append(args, "fleet", "open", row.Host, row.Repository.Path)
			}
			return exec.CommandContext(ctx, executable, args...), nil
		},

		CloneRemote: func(ctx context.Context, r tui.RemoteRow) (tui.OpenResult, string, error) {
			dest := filepath.Join(config.Expand(app.Cfg.Paths.ProjectRoot), r.Repo.Name)
			if _, err := os.Stat(dest); err == nil {
				return tui.OpenResult{}, "", fmt.Errorf("%s already exists; add it to scan_roots or clone somewhere explicit",
					config.Contract(dest))
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return tui.OpenResult{}, "", err
			}
			if _, err := gitx.Run(ctx, filepath.Dir(dest), "clone", r.Repo.CloneURL, dest); err != nil {
				return tui.OpenResult{}, "", err
			}
			handle, err := openCheckout(ctx, rt, dest, r.Repo.Name)
			if err != nil {
				return tui.OpenResult{}, "", fmt.Errorf("cloned to %s, but could not open it: %w", config.Contract(dest), err)
			}
			if rt.Name() == "none" {
				return tui.OpenResult{Status: "cloned " + r.Repo.FullName + " to " + config.Contract(dest)}, dest, nil
			}
			return tui.OpenResult{
				Status:        fmt.Sprintf("cloned %s to %s; open in %s (%s)", r.Repo.FullName, config.Contract(dest), rt.Name(), handle.Handle),
				RuntimeHandle: handle.Handle,
			}, dest, nil
		},

		Park: func(ctx context.Context, t *task.Task, next string) (string, error) {
			if next != "" {
				t.Next = next
			}
			runtimeForTask(app, t) // normalize empty handle/name provenance
			if t.RuntimeHandle != "" {
				if _, _, err := closeTaskRuntime(ctx, app, t, checkoutOf(t)); err != nil {
					return "", err
				}
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
			annotate(app, runtimeForTask(app, t), t)
			return nil
		},

		// Start is the bridge from "here are my repositories" to "here is what
		// I am working on" — the step that otherwise means dropping out of the
		// dashboard to type a command.
		Start: func(ctx context.Context, r tui.RepoRow, name string) (string, error) {
			spec, err := buildStartSpecForRepository(ctx, app, r.Repo, startRequest{
				Name: name, Mode: task.ModeWorktree,
			})
			if err != nil {
				return "", err
			}
			started, err := executeStartSpec(ctx, app, spec, nil)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("started %s on %s", name, started.Task.Branch), nil
		},

		StartDirect: func(ctx context.Context, r tui.RepoRow, name string) (string, error) {
			if err := guardSharedCheckout(ctx, app, rt, r.Repo.Path); err != nil {
				return "", err
			}
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
			setTaskRuntime(t, rt, handle)
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
			r, _, err := repo.Resolve(ctx, app.Cfg.DiscoveryRoots(), repoName)
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

		EditFleetConfig: func() (*exec.Cmd, error) {
			return fleetConfigEditorProcess(app, "")
		},

		// loadFleetConfig applies defaults and re-runs the private-mode check,
		// so an edit that loosens remotes.toml's permissions while it holds a
		// plaintext password is caught here rather than at the next fan-out.
		ValidateFleetConfig: func() error {
			_, err := loadFleetConfig(app)
			return err
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
	if cached, ok, stale := cachedRemoteRows(app); ok {
		model = model.WithRemotes(cached).WithRemotesStale(stale)
	}
	if cached := cachedFleetRows(app); len(cached) > 0 {
		model = model.WithFleet(cached)
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
		if handle := m.Activation(); handle != "" {
			return activateRuntime(ctxOf(), rt, handle)
		}
	}
	return nil
}

func collectTries(ctx context.Context, app *App, rt runtime.Runtime, includeAll bool) ([]tui.TryRow, error) {
	options := experiment.ListOptions{All: includeAll}
	return collectTriesWithOptions(ctx, app, rt, options, nil, false)
}

func collectTriesWithOptions(ctx context.Context, app *App, rt runtime.Runtime, options experiment.ListOptions,
	sessions []runtime.Session, sessionsSet bool) ([]tui.TryRow, error) {
	service, err := newExperimentService(app)
	if err != nil {
		return nil, err
	}
	items, diagnostics, listErr := service.List(ctx, options)
	warnExperimentDiagnostics(app, diagnostics)
	if !sessionsSet {
		sessions, _ = rt.List(ctx)
	}
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
			result.Status += fmt.Sprintf(" in %s (%s)", rt.Name(), handle.Handle)
			result.RuntimeHandle = handle.Handle
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
	Sessions     []runtime.Session
	SessionsSet  bool
}

// collectRepos builds the default repository view: what exists, plus how much
// of it is in flight, excluding cataloged active/deprecated Tries.
func collectRepos(ctx context.Context, app *App, rt runtime.Runtime) ([]tui.RepoRow, error) {
	return collectReposWithOptions(ctx, app, rt, repoCollectOptions{})
}

func collectReposWithOptions(ctx context.Context, app *App, rt runtime.Runtime, options repoCollectOptions) ([]tui.RepoRow, error) {
	repos, err := repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
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
	sessions := options.Sessions
	if !options.SessionsSet {
		sessions, _ = rt.List(ctx)
	}
	notesByRepo := map[string][]*note.Note{}
	if app.Notes != nil {
		allNotes, noteErr := app.Notes.List("")
		if noteErr != nil {
			return nil, noteErr
		}
		for _, n := range allNotes {
			notesByRepo[n.RepositoryID] = append(notesByRepo[n.RepositoryID], n)
		}
	}

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
			if row.Asset != nil {
				repoNotes := notesByRepo[row.Asset.ID]
				row.NoteCount = len(repoNotes)
				if len(repoNotes) > 0 {
					row.LatestNote = repoNotes[0]
				}
			}
			if r.HasGit {
				row.Topology, row.TopologyErr = gitx.RecoveryTopologyOf(ctx, r.Path)
			}
			row.Tasks = append(row.Tasks, byRepo[r.Path]...)
			if r.RealPath != "" && r.RealPath != r.Path {
				row.Tasks = append(row.Tasks, byRepo[r.RealPath]...)
			}
			row.Context = inventory.CollectRepoContext(ctx, r, row.Tasks, sessions, rt.Name())
			row.LastActivity = row.Context.LastActivity
			row.Worktrees = row.Context.WorktreeCount
			if main, ok := row.Context.Main(); ok {
				row.Status = main.Status
			}
			if !r.Bare {
				row.RemoteForge, row.RemoteName = forge.IdentityFromURL(gitx.RemoteFromConfig(r.CommonDir, "origin"))
			}
			if live := row.Context.Sessions(); len(live) > 0 {
				row.Live, row.Runtime = true, rt.Name()
				row.RuntimeHandle, row.RuntimeStatus = live[0].Handle, live[0].AgentStatus
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
func cachedRemoteRows(app *App) ([]tui.RemoteRow, bool, bool) {
	cache, ok := forge.LoadCacheAny(remoteCachePath())
	if !ok {
		return nil, false, false
	}
	out := make([]tui.RemoteRow, 0, len(cache.Repos))
	for _, rr := range cache.Repos {
		out = append(out, tui.RemoteRow{Repo: rr})
	}
	applyCatalogRemoteMatches(app, out)
	return out, true, !cache.Fresh(app.Cfg.Forge.CacheTTL.Duration)
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

func cachedRemotesAny(ctx context.Context, app *App) ([]tui.RemoteRow, forge.Cache, bool) {
	cache, ok := forge.LoadCacheAny(remoteCachePath())
	if !ok {
		return nil, forge.Cache{}, false
	}
	return matchRemoteLocals(ctx, app, cache.Repos), cache, true
}

// collectRemotes aggregates configured forge CLIs, caches the normalised response, then
// marks remotes that already have a checkout under the configured scan roots.
// Calls run concurrently so one slow forge does not serialise the other.
func collectRemotes(ctx context.Context, app *App) ([]tui.RemoteRow, error) {
	type result struct {
		kind  forge.Kind
		repos []forge.RemoteRepo
		err   error
	}
	providers := configuredForges(app)
	ch := make(chan result, len(providers))
	var wg sync.WaitGroup
	available := 0
	var unavailable []error
	expected := map[forge.Kind]bool{}
	var unavailableKinds []forge.Kind
	for _, f := range providers {
		if !f.Available() {
			// GitHub and GitLab remain opportunistic, but an explicit Azure
			// target should report why it did not contribute any repositories.
			if f.Kind() == forge.AzureDevOps {
				unavailable = append(unavailable, &forge.ErrNoCLI{Kind: forge.AzureDevOps, Bin: "az"})
				expected[f.Kind()] = true
				unavailableKinds = append(unavailableKinds, f.Kind())
			}
			continue
		}
		expected[f.Kind()] = true
		available++
		wg.Add(1)
		go func(f forge.Forge) {
			defer wg.Done()
			r, err := f.ListRepos(ctx)
			ch <- result{kind: f.Kind(), repos: r, err: err}
		}(f)
	}
	wg.Wait()
	close(ch)
	if available == 0 {
		if cached, _, ok := cachedRemotesAny(ctx, app); ok {
			if len(unavailable) > 0 {
				return cached, errors.Join(unavailable...)
			}
			return cached, fmt.Errorf("no supported forge CLI is installed")
		}
		if len(unavailable) > 0 {
			return nil, errors.Join(unavailable...)
		}
		return nil, fmt.Errorf("no supported forge CLI is installed")
	}

	old, _ := forge.LoadCacheAny(remoteCachePath())
	byProvider := map[forge.Kind][]forge.RemoteRepo{}
	statuses := map[forge.Kind]forge.ProviderStatus{}
	for _, r := range old.Repos {
		byProvider[r.Forge] = append(byProvider[r.Forge], r)
	}
	for kind, status := range old.Providers {
		statuses[kind] = status
	}
	errs := append([]error(nil), unavailable...)
	now := time.Now().UTC()
	for _, kind := range unavailableKinds {
		status := statuses[kind]
		status.Error = "provider CLI unavailable"
		statuses[kind] = status
	}
	for res := range ch {
		if res.err != nil {
			errs = append(errs, res.err)
			status := statuses[res.kind]
			status.Error = res.err.Error()
			if len(byProvider[res.kind]) == 0 && len(res.repos) > 0 {
				byProvider[res.kind] = res.repos
				status.FetchedAt = now
				status.Complete = false
			}
			statuses[res.kind] = status
			continue
		}
		byProvider[res.kind] = res.repos
		statuses[res.kind] = forge.ProviderStatus{FetchedAt: now, Complete: true}
	}
	var remoteRepos []forge.RemoteRepo
	complete := true
	providerStatuses := map[forge.Kind]forge.ProviderStatus{}
	for kind := range expected {
		status, ok := statuses[kind]
		if !ok || !status.Complete {
			complete = false
		}
		if ok {
			providerStatuses[kind] = status
		}
		remoteRepos = append(remoteRepos, byProvider[kind]...)
	}
	sortRemoteRepos(remoteRepos)
	if len(remoteRepos) > 0 {
		_ = forge.SaveCacheState(remoteCachePath(), forge.Cache{
			Version: forge.CacheVersion, FetchedAt: now, Complete: complete,
			Providers: providerStatuses, Repos: remoteRepos,
		})
	}
	return matchRemoteLocals(ctx, app, remoteRepos), errors.Join(errs...)
}

func sortRemoteRepos(repos []forge.RemoteRepo) {
	sort.SliceStable(repos, func(i, j int) bool {
		if !repos[i].UpdatedAt.Equal(repos[j].UpdatedAt) {
			return repos[i].UpdatedAt.After(repos[j].UpdatedAt)
		}
		return repos[i].Label() < repos[j].Label()
	})
}

func configuredForges(app *App) []forge.Forge {
	providers := forge.All()
	if app == nil || len(app.Cfg.Forge.AzureDevOps) == 0 {
		return providers
	}
	targets := make([]forge.AzureDevOpsTarget, 0, len(app.Cfg.Forge.AzureDevOps))
	for _, target := range app.Cfg.Forge.AzureDevOps {
		targets = append(targets, forge.AzureDevOpsTarget{
			Organization: target.Organization,
			Project:      target.Project,
		})
	}
	return forge.All(forge.NewAzureDevOps(targets))
}

func matchRemoteLocals(ctx context.Context, app *App, remoteRepos []forge.RemoteRepo) []tui.RemoteRow {
	locals, _ := repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
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

func prepareTUINoteEdit(app *App, n *note.Note) (tui.NoteEdit, error) {
	tmp, err := os.CreateTemp("", "dev-note-*.md")
	if err != nil {
		return tui.NoteEdit{}, err
	}
	path := tmp.Name()
	if _, err := io.WriteString(tmp, n.Body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(path)
		return tui.NoteEdit{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return tui.NoteEdit{}, err
	}
	proc, _, err := editorProcess(path, "")
	if err != nil {
		_ = os.Remove(path)
		return tui.NoteEdit{}, err
	}
	return tui.NoteEdit{
		Command: proc,
		Complete: func(runErr error) error {
			if runErr != nil {
				_ = os.Remove(path)
				return runErr
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read edited body (preserved at %s): %w", path, err)
			}
			if strings.TrimSpace(string(body)) == "" {
				_ = os.Remove(path)
				return fmt.Errorf("edited note body is empty; not saved")
			}
			_, err = app.Notes.Update(context.Background(), n.ID, n.Revision(), string(body), n.Tags)
			if err != nil {
				return fmt.Errorf("save edited note: %w; edited body preserved at %s", err, path)
			}
			_ = os.Remove(path)
			return nil
		},
	}, nil
}
