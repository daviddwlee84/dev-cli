package cli

import (
	"context"
	"crypto/sha256"
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
	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/daviddwlee84/dev-cli/internal/stats"
	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/daviddwlee84/dev-cli/internal/wt"
	"github.com/spf13/cobra"
)

const tuiCapabilityFileMaxBytes = agentmcp.DefaultMaxFileBytes

func newTUICmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Interactive dashboard over the inventory",
		Long: `Browse and act on your work in progress.

Shows exactly what "dev ls" shows, from the same code path, plus the ability
to open, park and annotate a task without retyping its name.

Seven lists, switched with tab:

  TASKS   change streams dev is tracking — what am I working on
  REPOS   durable repositories under the scan roots — what do I have here
  FLEET   repositories and active work across configured SSH machines
  TRY     scratch experiments and retained lifecycle history
  REMOTE  repositories visible through configured forge CLIs — what can I clone/open
  SKILLS  startup-context/global agent skills; A toggles all repositories
  MCP     startup-context static declarations; A toggles all repositories

Navigation is vim-style, with arrows and mouse alongside:

  j k        move                 ctrl+d ctrl+u   half a page
  g G        top / bottom         h l / tab       previous / next view
  /          filter as you type   esc             clear, then quit
  left click select row/tab       wheel            move three rows
  right click selected row actions; click never opens a row directly

Actions depend on the list:

  TASKS   enter open · p park · c edit next
  REPOS   enter ad hoc · space worktrees · m metadata · s worktree task · d direct task
  FLEET   enter Herdr/SSH open · Git changes are read-only here
  TRY     enter open · n create · space lifecycle/metadata actions
  REMOTE  enter open local · c clone after confirmation
  SKILLS  a add · c check · u update · e open file · y copy · A context/all
  MCP     e open config · y copy · A context/all · r reload static declarations

  y       REPOS yy/yp/yb/ys/yw; SKILLS/MCP yp path · ys summary · yf raw file
  H       selected repo heatmap; b backfills it when empty
  e       edit the current view's config/file; returning reloads that source
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
				if tool.Probe != nil && !tool.Probe(ctxOf()) {
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

func tuiRemoteCloneDestination(app *App, name string) (string, error) {
	projectRoot := config.Expand(app.Cfg.Paths.ProjectRoot)
	destination, err := pathx.JoinChild(projectRoot, name)
	if err != nil {
		return "", fmt.Errorf("remote repository name %q is not a safe clone destination: %w", name, err)
	}
	return destination, nil
}

func cloneRemoteFromTUI(ctx context.Context, app *App, row tui.RemoteRow) (string, error) {
	destination, err := tuiRemoteCloneDestination(app, row.Repo.Name)
	if err != nil {
		return "", err
	}
	discoverable, err := repositoryPathDiscoverable(app.Cfg, destination)
	if err != nil {
		return "", fmt.Errorf("verify REMOTE clone destination %s: %w", config.Contract(destination), err)
	}
	if !discoverable {
		return "", fmt.Errorf("%s is outside paths.scan_roots/repo_paths; add paths.project_root to scan_roots before cloning from REMOTE",
			config.Contract(destination))
	}
	if _, err := os.Lstat(destination); err == nil {
		return destination, fmt.Errorf("%s already exists; add it to scan_roots/repo_paths or move it before cloning", config.Contract(destination))
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect clone destination %s: %w", config.Contract(destination), err)
	}
	acquired, err := repo.Acquire(ctx, repo.AcquireRequest{
		Kind: repo.AcquireClone, Name: row.Repo.Name,
		CloneRef: row.Repo.CloneURL, Destination: destination,
	})
	if err != nil {
		if _, statErr := os.Lstat(destination); statErr == nil {
			return destination, fmt.Errorf("%w; inspect the destination now present at %s before retrying", err, config.Contract(destination))
		}
		return "", err
	}
	return acquired.Path, nil
}

func startDirectFromTUI(ctx context.Context, app *App, rt runtime.Runtime, row tui.RepoRow, name string) (string, error) {
	if err := guardSharedCheckout(ctx, app, rt, row.Repo.Path); err != nil {
		return "", err
	}
	status, err := gitx.StatusOf(ctx, row.Repo.Path)
	if err != nil {
		return "", err
	}
	if status.Detached || status.Branch == "" {
		return "", fmt.Errorf("direct task needs a named branch; this repo has detached HEAD")
	}
	id := task.MakeID(row.Repo.Name, status.Branch)
	replaceDoneRevision := ""
	existing, err := existingTaskForStart(app.Tasks, id)
	if err != nil {
		return "", err
	}
	if existing != nil && existing.State != task.Done {
		return "", fmt.Errorf("task %s already exists (state %s)", existing.ID, existing.State)
	}
	if existing != nil {
		replaceDoneRevision = existing.Revision()
	}
	handle, err := rt.Open(ctx, row.Repo.Path, row.Repo.Name+"/"+name)
	if err != nil {
		return "", err
	}
	tracked := &task.Task{
		Name: name, Repo: row.Repo.Name, RepoPath: row.Repo.Path,
		Branch: status.Branch, Base: status.Branch, Mode: task.ModeDirect,
		State: task.Hot, Owner: config.Hostname(),
	}
	setTaskRuntime(tracked, rt, handle)
	if replaceDoneRevision != "" {
		err = app.Tasks.ReplaceDone(tracked, replaceDoneRevision)
	} else {
		err = app.Tasks.Save(tracked)
	}
	if err != nil {
		if handle.Created && handle.Handle != "" {
			_ = rt.Close(ctx, handle.Handle)
		}
		return "", err
	}
	annotate(app, rt, tracked)
	return fmt.Sprintf("tracking %s directly on %s; no branch/worktree created", name, status.Branch), nil
}

func runTUI(app *App) error {
	app.traceTUI = true
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	finishSetup := app.trace.Start(perftrace.TUISetup, perftrace.Fields{})
	// --color, NO_COLOR and TERM=dumb governed every other surface but stopped
	// at the dashboard, which resolved its own palette independently.
	tui.SetColorEnabled(app.outStyle().enabled)
	appState := newTUIAppState(app)
	runtimeResolver := newTUIRuntimeResolver(app)
	projectRootResolver := newTUIProjectRootResolver(app.trace, runCtx)
	localLoader := newTUILocalLoader(app, runtimeResolver)
	localLoader.current = appState.Current

	reload := func(ctx context.Context) ([]inventory.Row, error) {
		rt, err := runtimeResolver.Resolve(ctx)
		if err != nil {
			return nil, err
		}
		tasks, err := appState.Current().Tasks.List()
		if err != nil {
			return nil, err
		}
		return inventory.Collect(ctx, tasks, rt, inventory.Options{}), nil
	}
	reloadRepos := func(ctx context.Context) ([]tui.RepoRow, error) {
		rt, err := runtimeResolver.Resolve(ctx)
		if err != nil {
			return nil, err
		}
		// Keep Try repositories in the model's local snapshot so cached REMOTE
		// rows can be matched and cleared correctly. The REPOS view filters them.
		return collectReposWithOptions(ctx, appState.Current(), rt, repoCollectOptions{IncludeTries: true})
	}
	reloadRemote := func(ctx context.Context, locals []tui.RepoRow) ([]tui.RemoteRow, error) {
		return collectRemotesForRows(ctx, appState.Current(), locals)
	}
	reloadFleet := func(ctx context.Context, locals []tui.RepoRow) ([]tui.FleetRow, error) {
		rt, err := runtimeResolver.Resolve(ctx)
		if err != nil {
			return nil, err
		}
		snapshot := fleetSnapshotFromRepoRows(locals, rt.Name())
		results, _, err := collectFleet(ctx, appState.Current(), fleetCollectOptions{LocalSnapshot: &snapshot})
		return fleetRows(results), err
	}
	capabilityTargets := func(ctx context.Context, locals []tui.RepoRow, scope tui.CapabilityScope) ([]agenttarget.Target, error) {
		current, err := projectRootResolver.ResolveTarget(ctx)
		if err != nil {
			return nil, err
		}
		return tuiCapabilityTargets(locals, current, scope), nil
	}
	reloadSkills := func(ctx context.Context, locals []tui.RepoRow, scope tui.CapabilityScope) ([]agentskill.Skill, error) {
		targets, err := capabilityTargets(ctx, locals, scope)
		if err != nil {
			return nil, err
		}
		result, err := inventory.CollectAgentSkills(ctx, targets, inventory.AgentSkillOptions{})
		if err != nil {
			return result.Skills, err
		}
		if len(result.Diagnostics) > 0 {
			return result.Skills, tui.LoadWarning{Message: fmt.Sprintf("%d skill inventory diagnostic(s); run `dev skill list --all` for details", len(result.Diagnostics))}
		}
		return result.Skills, nil
	}
	reloadMCP := func(ctx context.Context, locals []tui.RepoRow, scope tui.CapabilityScope) ([]agentmcp.Declaration, error) {
		targets, err := capabilityTargets(ctx, locals, scope)
		if err != nil {
			return nil, err
		}
		result, err := agentmcp.Scan(ctx, targets)
		if err != nil {
			return result.Declarations, err
		}
		if len(result.Diagnostics) > 0 {
			return result.Declarations, tui.LoadWarning{Message: fmt.Sprintf("%d MCP inventory diagnostic(s); run `dev mcp list --all` for details", len(result.Diagnostics))}
		}
		return result.Declarations, nil
	}
	reloadTries := func(ctx context.Context, includeAll bool) ([]tui.TryRow, error) {
		rt, err := runtimeResolver.Resolve(ctx)
		if err != nil {
			return nil, err
		}
		return collectTries(ctx, appState.Current(), rt, includeAll)
	}
	openResolved := func(ctx context.Context, dir, label, statusLabel string) (tui.OpenResult, error) {
		rt, err := runtimeResolver.Resolve(ctx)
		if err != nil {
			return tui.OpenResult{}, err
		}
		handle, err := openCheckout(ctx, rt, dir, label)
		if err != nil {
			return tui.OpenResult{}, err
		}
		if rt.Name() == "none" {
			return tui.OpenResult{Directory: dir}, nil
		}
		if statusLabel == "" {
			statusLabel = label
		}
		return tui.OpenResult{
			Status:        fmt.Sprintf("%s open in %s (%s)", statusLabel, rt.Name(), handle.Handle),
			RuntimeHandle: handle.Handle,
		}, nil
	}

	actions := tui.Actions{
		Reload:                reload,
		ReloadRepos:           reloadRepos,
		ReloadRemoteWithRepos: reloadRemote,
		ReloadFleetWithRepos:  reloadFleet,
		ReloadSkillsWithRepos: reloadSkills,
		ReloadMCPWithRepos:    reloadMCP,
		LoadRemoteCache: func(context.Context) tui.RemoteCacheResult {
			rows, found, stale := cachedRemoteRows(appState.Current())
			return tui.RemoteCacheResult{Rows: rows, Found: found, Stale: stale}
		},
		LoadFleetCache: func(context.Context) tui.FleetCacheResult {
			rows := cachedFleetRows(appState.Current())
			return tui.FleetCacheResult{Rows: rows, Found: len(rows) > 0}
		},
		AfterFirstView: func(context.Context) {
			if app.deferredReleaseRefresh {
				app.refreshReleaseDetached()
			}
		},
		CheckSkills: func(ctx context.Context, rows []agentskill.Skill) []agentskill.Skill {
			return agentskill.CheckUpdates(ctx, rows)
		},
		AddSkill: func() (*agentskill.MutationCommand, error) {
			projectRoot, ok := projectRootResolver.Current()
			if !ok {
				return nil, errors.New("project root is still loading")
			}
			return agentskill.AddCommand(context.Background(), projectRoot, agentskill.DefaultSource)
		},
		UpdateSkill: func(row agentskill.Skill) (*agentskill.MutationCommand, error) {
			projectRoot := row.Checkout
			if row.Scope == agentskill.ScopeGlobal || projectRoot == "" {
				var ok bool
				projectRoot, ok = projectRootResolver.Current()
				if !ok {
					return nil, errors.New("project root is still loading")
				}
			}
			managedName := row.Name
			if row.Lock != nil {
				managedName = row.Lock.Name
			}
			return agentskill.UpdateCommand(context.Background(), projectRoot, managedName, row.Scope)
		},
		Repos: tui.RepoActions{
			Create: func() (*exec.Cmd, error) {
				return tuiRepoNewProcess(appState.Current())
			},
			Patch: func(ctx context.Context, row tui.RepoRow, tags []string, note string) (string, error) {
				active := appState.Current()
				remove := []string(nil)
				if row.Asset != nil {
					remove = append(remove, row.Asset.Tags...)
				}
				marked, err := patchRepositoryCatalog(ctx, active, row.Repo, tags, remove, &note)
				if err != nil {
					return "", err
				}
				return "updated metadata for " + marked.Title(), nil
			},
		},
		Tries: tui.TryActions{
			Reload: reloadTries,
			Apply: func(ctx context.Context, request tui.TryRequest) (tui.TryActionResult, error) {
				rt, err := runtimeResolver.Resolve(ctx)
				if err != nil {
					return tui.TryActionResult{}, err
				}
				return applyTryAction(ctx, appState.Current(), rt, request)
			},
		},
		Notes: tui.NoteActions{
			List: func(ctx context.Context, target tui.NoteTarget) ([]*note.Note, error) {
				active := appState.Current()
				entry, _, err := ensureNoteRepository(ctx, active, target)
				if err != nil {
					return nil, err
				}
				return active.Notes.List(entry.ID)
			},
			Search: func(ctx context.Context, target tui.NoteTarget, query string) ([]*note.Note, error) {
				active := appState.Current()
				entry, _, err := ensureNoteRepository(ctx, active, target)
				if err != nil {
					return nil, err
				}
				return active.Notes.Search(query, entry.ID, 100)
			},
			Add: func(ctx context.Context, target tui.NoteTarget, body string) (string, error) {
				active := appState.Current()
				entry, _, err := ensureNoteRepository(ctx, active, target)
				if err != nil {
					return "", err
				}
				n, err := active.Notes.Add(ctx, entry.ID, entry.Title(), body, nil)
				if err != nil {
					return "", err
				}
				return "added note " + n.ID[:8] + " to " + entry.Title(), nil
			},
			Delete: func(ctx context.Context, n *note.Note) (string, error) {
				if err := appState.Current().Notes.Delete(ctx, n.ID); err != nil {
					return "", err
				}
				return "deleted note " + n.ID[:8], nil
			},
			Edit: func(n *note.Note) (tui.NoteEdit, error) {
				return prepareTUINoteEdit(appState.Current(), n)
			},
		},
		Sizes: tui.SizeActions{
			Start: func(ctx context.Context, targets []diskusage.Target, force bool) diskusage.Load {
				return appState.Current().Sizes.Start(ctx, targets, force)
			},
			Cancel: func(loadID uint64) { appState.Current().Sizes.Cancel(loadID) },
		},
		Local:       tui.LocalActions{Start: localLoader.Start},
		Tools:       externalTools(app),
		RepoColumns: app.Cfg.EffectiveRepoColumns(),
		RepoSort:    app.Cfg.EffectiveRepoSort(),
		RepoReverse: app.Cfg.TUI.Repos.Reverse,
		Copy:        clipboard.WriteAll,
		ReadFile:    readTUICapabilityFile,
		EditFile:    prepareTUICapabilityEdit,

		// Open is navigation-only. The model rejects missing/unregistered and
		// cold worktree tasks before this callback so reconciliation and writer
		// ownership remain explicit `dev sweep` / `dev resume` operations.
		Open: func(ctx context.Context, t *task.Task) (tui.OpenResult, error) {
			checkout := checkoutOf(t)
			if _, err := os.Stat(checkout); err != nil {
				return tui.OpenResult{}, fmt.Errorf("%s has no checkout — run `dev resume %s`", t.Title(), t.ID)
			}
			// Enter is navigation only. Claiming a writer is an explicit
			// `dev resume`/start action with collision checks.
			return openResolved(ctx, checkout, t.Title(), "")
		},

		OpenRepo: func(ctx context.Context, r tui.RepoRow) (tui.OpenResult, error) {
			return openResolved(ctx, r.Repo.Path, r.Repo.Name, "")
		},

		OpenCheckout: func(ctx context.Context, r tui.RepoRow, checkout inventory.RepoCheckout) (tui.OpenResult, error) {
			branch := checkout.Branch()
			if branch == "" {
				branch = filepath.Base(checkout.Worktree.Path)
			}
			label := r.Repo.Name + "/" + branch
			return openResolved(ctx, checkout.Worktree.Path, label, "")
		},

		OpenRemote: func(ctx context.Context, r tui.RemoteRow) (tui.OpenResult, error) {
			if r.LocalPath == "" {
				return tui.OpenResult{}, fmt.Errorf("%s has no local checkout; press c to clone it", r.Repo.FullName)
			}
			return openResolved(ctx, r.LocalPath, r.Repo.Name, r.Repo.FullName)
		},

		OpenFleet: func(ctx context.Context, row tui.FleetRow) (*exec.Cmd, error) {
			active := appState.Current()
			if row.Repository == nil {
				return nil, fmt.Errorf("host %s has no repository selected", row.Host)
			}
			executable, err := os.Executable()
			if err != nil {
				return nil, err
			}
			args := []string{}
			if active.configPath != "" {
				args = append(args, "--config", active.configPath)
			}
			if active.remotesPath != "" {
				args = append(args, "--remotes", active.remotesPath)
			}
			if row.Local {
				args = append(args, "repo", "open", row.Repository.Path)
			} else {
				args = append(args, "fleet", "open", row.Host, row.Repository.Path)
			}
			return exec.CommandContext(ctx, executable, args...), nil
		},

		CloneRemote: func(ctx context.Context, r tui.RemoteRow) (string, error) {
			return cloneRemoteFromTUI(ctx, appState.Current(), r)
		},

		Park: func(ctx context.Context, selected *task.Task, next string) (string, error) {
			active := appState.Current()
			rt, err := runtimeResolver.Resolve(ctx)
			if err != nil {
				return "", err
			}
			actionApp := *active
			actionApp.runtimeInstance = rt
			execution, err := executeTaskLifecycle(ctx, &actionApp, func() (*task.Task, error) {
				return actionApp.Tasks.Get(selected.ID)
			}, flow.ParkWarmOptions{Next: next})
			if err != nil {
				return "", err
			}
			return execution.Task.Title() + " parked warm — worktree and branch kept", nil
		},

		SetNext: func(ctx context.Context, selected *task.Task, next string) error {
			active := appState.Current()
			current, err := active.Tasks.GetRecord(selected.ID)
			if err != nil {
				return err
			}
			updated := current.Task
			updated.Next = next
			persisted, err := active.Tasks.Update(ctx, &updated, current.Revision)
			if err != nil {
				return err
			}
			annotate(active, runtimeForTask(active, &persisted.Task), &persisted.Task)
			return nil
		},

		// Start is the bridge from "here are my repositories" to "here is what
		// I am working on" — the step that otherwise means dropping out of the
		// dashboard to type a command.
		Start: func(ctx context.Context, r tui.RepoRow, name string) (string, error) {
			active := appState.Current()
			rt, err := runtimeResolver.Resolve(ctx)
			if err != nil {
				return "", err
			}
			actionApp := *active
			actionApp.runtimeInstance = rt
			spec, err := buildStartSpecForRepository(ctx, &actionApp, r.Repo, startRequest{
				Name: name, Mode: task.ModeWorktree,
			})
			if err != nil {
				return "", err
			}
			started, err := executeStartSpec(ctx, &actionApp, spec, nil)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("started %s on %s", name, started.Task.Branch), nil
		},

		StartDirect: func(ctx context.Context, r tui.RepoRow, name string) (string, error) {
			active := appState.Current()
			rt, err := runtimeResolver.Resolve(ctx)
			if err != nil {
				return "", err
			}
			return startDirectFromTUI(ctx, active, rt, r, name)
		},

		LoadStats: func(ctx context.Context, repoName string) (tui.StatsPanel, error) {
			active := appState.Current()
			store, err := stats.Open(stats.Path(active.Cfg.StateDir()))
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
			active := appState.Current()
			r, _, err := repo.Resolve(ctx, active.Cfg.DiscoveryRoots(), repoName)
			if err != nil {
				return err
			}
			store, err := stats.Open(stats.Path(active.Cfg.StateDir()))
			if err != nil {
				return err
			}
			defer store.Close()
			_, err = stats.BackfillGit(ctx, store, []repo.Repo{r}, time.Now().AddDate(-1, 0, 0), "")
			return err
		},

		EditConfig: func() (*exec.Cmd, error) {
			proc, _, _, err := configEditorProcess(appState.Current(), "")
			return proc, err
		},

		EditFleetConfig: func() (*exec.Cmd, error) {
			return fleetConfigEditorProcess(appState.Current(), "")
		},

		// loadFleetConfig applies defaults and re-runs the private-mode check,
		// so an edit that loosens remotes.toml's permissions while it holds a
		// plaintext password is caught here rather than at the next fan-out.
		ValidateFleetConfig: func() error {
			_, err := loadFleetConfig(appState.Current())
			return err
		},

		ReloadConfig: func(ctx context.Context) (tui.ConfigUpdate, string, error) {
			currentRuntime, err := runtimeResolver.Resolve(ctx)
			if err != nil {
				return tui.ConfigUpdate{}, "", err
			}
			oldRuntime := currentRuntime.Name()
			next, err := appState.Prepare(ctx)
			if err != nil {
				return tui.ConfigUpdate{}, "", err
			}
			status := "config + data reloaded"
			if nextRuntime := next.Runtime().Name(); nextRuntime != oldRuntime {
				status += fmt.Sprintf("; restart TUI to switch runtime %s → %s", oldRuntime, nextRuntime)
			}
			return tui.ConfigUpdate{
				Apply:       func() { appState.Commit(next) },
				Tools:       externalTools(next),
				RepoColumns: next.Cfg.EffectiveRepoColumns(),
				RepoSort:    next.Cfg.EffectiveRepoSort(),
				RepoReverse: next.Cfg.TUI.Repos.Reverse,
			}, status, nil
		},
	}

	// Enter the alternate screen immediately. Local inventory is loaded by
	// Init in the background rather than making the terminal appear frozen
	// while dozens of repos are probed.
	model := tui.New(actions, nil, nil).WithTrace(app.trace).WithContext(runCtx).BeginLoading()
	finishSetup(perftrace.OutcomeSuccess)
	app.trace.Mark(perftrace.TUIProgramRunBegin, perftrace.Fields{})
	final, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	cancelRun()
	app.finishTrace()
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
			rt, resolveErr := runtimeResolver.Resolve(ctxOf())
			if resolveErr != nil {
				return resolveErr
			}
			return activateRuntime(ctxOf(), rt, handle)
		}
	}
	return nil
}

func tuiRepoNewProcess(app *App) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, 8)
	if app.configPath != "" {
		args = append(args, "--config", app.configPath)
	}
	if app.scaffoldsPath != "" {
		args = append(args, "--scaffolds", app.scaffoldsPath)
	}
	args = append(args, "repo", "new", "--handoff", "stay")
	return exec.Command(executable, args...), nil
}

func readTUICapabilityFile(ctx context.Context, path string) (string, error) {
	resolved, err := pathx.Canonical(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return "", err
	}
	defer root.Close()
	body, _, err := safefile.ReadStableRegular(ctx, root, filepath.Base(resolved), nil, tuiCapabilityFileMaxBytes)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// tuiCapabilityTargets makes SKILLS and MCP describe either what an agent
// launched from the startup context would see or every accepted repository.
// Global sources are still scanned once by the domain collectors.
func tuiCapabilityTargets(locals []tui.RepoRow, current agenttarget.Target, scope tui.CapabilityScope) []agenttarget.Target {
	repositories := make([]repo.Repo, 0, len(locals))
	for _, row := range locals {
		if !row.IsTry() {
			repositories = append(repositories, row.Repo)
		}
	}
	targets := agenttarget.FromRepositories(repositories)
	if scope == tui.CapabilityAllRepositories {
		return agenttarget.WithCurrent(targets, current)
	}
	if current.CheckoutRoot != "" && current.CommonDir != "" &&
		!sameCleanPath(current.CheckoutRoot, current.CommonDir) {
		return []agenttarget.Target{agenttarget.ReconcileCurrent(targets, current)}
	}
	return agenttarget.WithCurrent(targets, current)
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
	SessionsErr  error
	SessionsSet  bool
	Tasks        []*task.Task
	TasksSet     bool
	Repos        []repo.Repo
	ReposSet     bool
	Limiter      *inventory.Limiter
}

// collectRepos builds the default repository view: what exists, plus how much
// of it is in flight, excluding cataloged active/deprecated Tries.
func collectRepos(ctx context.Context, app *App, rt runtime.Runtime) ([]tui.RepoRow, error) {
	return collectReposWithOptions(ctx, app, rt, repoCollectOptions{})
}

func collectReposWithOptions(ctx context.Context, app *App, rt runtime.Runtime, options repoCollectOptions) ([]tui.RepoRow, error) {
	repos := options.Repos
	var err error
	if !options.ReposSet {
		repos, err = repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
		if err != nil {
			return nil, err
		}
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
	tasks := options.Tasks
	var taskDiagnostics []task.Diagnostic
	if !options.TasksSet {
		tasks, taskDiagnostics, err = app.Tasks.ListWithDiagnostics()
		if err != nil {
			return nil, err
		}
	}
	var taskInventoryErr error
	for _, diagnostic := range taskDiagnostics {
		taskInventoryErr = errors.Join(taskInventoryErr, diagnostic)
	}
	byRepo := map[string][]*task.Task{}
	for _, tracked := range tasks {
		byRepo[tracked.RepoPath] = append(byRepo[tracked.RepoPath], tracked)
	}
	sessions := options.Sessions
	runtimeErr := options.SessionsErr
	if !options.SessionsSet {
		sessions, runtimeErr = rt.List(ctx)
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
	// workers brings that down without forking hundreds at once. TUI callers
	// share the limiter with task enrichment.
	limiter := options.Limiter
	if limiter == nil {
		limiter = inventory.NewLimiter(8)
	}
	var wg sync.WaitGroup
	for i, r := range repos {
		wg.Add(1)
		go func(i int, r repo.Repo) {
			defer wg.Done()
			release, ok := limiter.Acquire(ctx)
			if !ok {
				return
			}
			defer release()

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
			row.Context.TaskErr = taskInventoryErr
			row.Context.RuntimeErr = runtimeErr
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
			Probe: commandRunnable(run, t.Interactive),
		})
	}
	return out
}

// commandRunnable resolves the command's first word on PATH, expanding a
// leading environment variable so "$EDITOR ." checks the editor itself. The
// returned probe is run by a bounded Bubble Tea command, never by View.
func commandRunnable(run string, interactive bool) func(context.Context) bool {
	return func(ctx context.Context) bool { return checkCommandRunnable(ctx, run, interactive) }
}

func checkCommandRunnable(ctx context.Context, run string, interactive bool) bool {
	if ctx == nil {
		ctx = context.Background()
	}
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
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		probe := exec.CommandContext(probeCtx, shellPath(), "-lic",
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

func remoteCacheSourceID(app *App) string {
	parts := []string{
		"forge-source-v1",
		"GH_HOST=" + os.Getenv("GH_HOST"),
		"GITLAB_HOST=" + os.Getenv("GITLAB_HOST"),
		"GLAB_HOST=" + os.Getenv("GLAB_HOST"),
	}
	for _, target := range app.Cfg.Forge.AzureDevOps {
		parts = append(parts, "azure="+target.Organization+"\x00"+target.Project)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("%x", sum[:])
}

// cachedRemoteRows reads only the private JSON cache — no repository scan or
// subprocess — so it is safe on the startup path. Local clone markers are
// filled when the background local inventory arrives.
func cachedRemoteRows(app *App) ([]tui.RemoteRow, bool, bool) {
	cache, ok := forge.LoadCacheAny(remoteCachePath())
	sourceID := remoteCacheSourceID(app)
	if !ok || cache.SourceID != sourceID {
		return nil, false, false
	}
	out := make([]tui.RemoteRow, 0, len(cache.Repos))
	for _, rr := range cache.Repos {
		out = append(out, tui.RemoteRow{Repo: rr})
	}
	return out, true, !cache.FreshFor(app.Cfg.Forge.CacheTTL.Duration, sourceID)
}

// cachedRemotesForRepoRows uses the local repo data the dashboard already
// collected, avoiding another process-spawning scan on startup.
func cachedRemotesForRepoRows(app *App, locals []tui.RepoRow) ([]tui.RemoteRow, bool) {
	cache, ok := forge.LoadCacheAny(remoteCachePath())
	if !ok || !cache.FreshFor(app.Cfg.Forge.CacheTTL.Duration, remoteCacheSourceID(app)) {
		return nil, false
	}
	return matchRemoteRepoRows(cache.Repos, locals), true
}

func matchRemoteRepoRows(remoteRepos []forge.RemoteRepo, locals []tui.RepoRow) []tui.RemoteRow {
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
	out := make([]tui.RemoteRow, 0, len(remoteRepos))
	for _, rr := range remoteRepos {
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
	return out
}

// cachedRemotes returns a fresh, already-local-matched cache for instant TUI
// navigation. Local paths are recomputed rather than cached because clones can
// move independently of the forge inventory.
func cachedRemotes(ctx context.Context, app *App) ([]tui.RemoteRow, bool) {
	cache, ok := forge.LoadCacheAny(remoteCachePath())
	if !ok || !cache.FreshFor(app.Cfg.Forge.CacheTTL.Duration, remoteCacheSourceID(app)) {
		return nil, false
	}
	return matchRemoteLocals(ctx, app, cache.Repos), true
}

func cachedRemotesAny(ctx context.Context, app *App) ([]tui.RemoteRow, forge.Cache, bool) {
	cache, ok := forge.LoadCacheAny(remoteCachePath())
	if !ok || (cache.SourceID != "" && cache.SourceID != remoteCacheSourceID(app)) {
		return nil, forge.Cache{}, false
	}
	return matchRemoteLocals(ctx, app, cache.Repos), cache, true
}

// collectRemotes aggregates configured forge CLIs, caches the normalised response, then
// marks remotes that already have a checkout under the configured scan roots.
// Calls run concurrently so one slow forge does not serialise the other.
type remoteCollectOptions struct {
	Locals       []tui.RepoRow
	LocalsSet    bool
	Providers    []forge.Forge
	ProvidersSet bool
}

func collectRemotes(ctx context.Context, app *App) ([]tui.RemoteRow, error) {
	return collectRemotesWithOptions(ctx, app, remoteCollectOptions{})
}

func collectRemotesForRows(ctx context.Context, app *App, locals []tui.RepoRow) ([]tui.RemoteRow, error) {
	return collectRemotesWithOptions(ctx, app, remoteCollectOptions{Locals: locals, LocalsSet: true})
}

func collectRemotesWithOptions(ctx context.Context, app *App, options remoteCollectOptions) ([]tui.RemoteRow, error) {
	type result struct {
		kind  forge.Kind
		repos []forge.RemoteRepo
		err   error
	}
	sourceID := remoteCacheSourceID(app)
	matchRows := func(repos []forge.RemoteRepo) []tui.RemoteRow {
		if options.LocalsSet {
			return matchRemoteRepoRows(repos, options.Locals)
		}
		return matchRemoteLocals(ctx, app, repos)
	}
	providers := options.Providers
	if !options.ProvidersSet {
		providers = configuredForges(app)
	}
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
		if cached, ok := forge.LoadCacheAny(remoteCachePath()); ok && cached.SourceID == sourceID {
			rows := matchRows(cached.Repos)
			if len(unavailable) > 0 {
				return rows, errors.Join(unavailable...)
			}
			return rows, fmt.Errorf("no supported forge CLI is installed")
		}
		if len(unavailable) > 0 {
			return nil, errors.Join(unavailable...)
		}
		return nil, fmt.Errorf("no supported forge CLI is installed")
	}

	old, _ := forge.LoadCacheAny(remoteCachePath())
	if old.SourceID != sourceID {
		old = forge.Cache{}
	}
	byProvider := map[forge.Kind][]forge.RemoteRepo{}
	statuses := map[forge.Kind]forge.ProviderStatus{}
	for _, r := range old.Repos {
		byProvider[r.Forge] = append(byProvider[r.Forge], r)
	}
	for kind, status := range old.Providers {
		statuses[kind] = status
	}
	errs := append([]error(nil), unavailable...)
	successful := 0
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
		successful++
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
	if successful > 0 {
		_ = forge.SaveCacheStateContext(ctx, remoteCachePath(), forge.Cache{
			Version: forge.CacheVersion, SourceID: sourceID, FetchedAt: now, Complete: complete,
			Providers: providerStatuses, Repos: remoteRepos,
		})
	}
	return matchRows(remoteRepos), errors.Join(errs...)
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
