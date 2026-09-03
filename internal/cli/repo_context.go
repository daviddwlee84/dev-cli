package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/assessment"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/repocontext"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/spf13/cobra"
)

func newRepoContextCmd(app *App) *cobra.Command {
	var jsonOut, refresh bool
	cmd := &cobra.Command{
		Use:   "context [repo]",
		Short: "Print agent-ready Git, worktree, runtime and task context",
		Long: `Print a deterministic Markdown handoff or a versioned JSON report for one repository.

Local Git, task and runtime facts are always collected live. Forge and configured
fleet-host observations use their private caches by default and expose provenance,
age and collection errors; --refresh may contact forge providers and fleet hosts.
With no repository argument, the repository containing the current directory is
used, and a linked-worktree current directory remains the selected checkout while
the report still covers the whole clone.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()
			repository, selectedPath, err := resolveRepoContextTarget(ctx, app, args)
			if err != nil {
				return err
			}
			local := collectLocalRepoContext(ctx, app, repository, selectedPath, true)
			now := time.Now().UTC()
			forgeFacts := collectRepoContextForge(ctx, app, refresh, now)
			fleetFacts := collectRepoContextFleet(ctx, app, refresh)
			report, err := repocontext.Build(repocontext.BuildInput{
				GeneratedAt: now, Context: local.Context,
				SelectedCheckout: local.SelectedCheckout, SelectionErr: local.SelectionErr,
				Topology: local.Topology, TopologyErr: local.TopologyErr,
				Runtimes: local.Runtimes, Forge: forgeFacts, Fleet: fleetFacts,
				Hostname: config.Hostname(),
			})
			if err != nil {
				return err
			}
			if jsonOut {
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(report)
			}
			legacy := inventory.FormatRepoContext(local.Context, -1)
			fmt.Fprint(app.Out, repocontext.FormatMarkdown(report, legacy))
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the additive schema-v1 JSON report")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "refresh external forge and configured fleet observations")
	return cmd
}

type localRepoContext struct {
	Context          inventory.RepoContext
	SelectedCheckout int
	SelectionErr     error
	Topology         gitx.RecoveryTopology
	TopologyErr      error
	Runtimes         []repocontext.RuntimeInput
}

// collectLocalRepoContext is the single CLI collector used by repo context and
// status. It executes only local Git/task/runtime probes; includeTopology adds
// repository-wide remote/ref facts but never performs fetch or other network I/O.
func collectLocalRepoContext(ctx context.Context, app *App, repository repo.Repo, selectedPath string, includeTopology bool) localRepoContext {
	var (
		tasks    = []*task.Task{}
		tasksErr error
	)
	if app.Tasks == nil {
		tasksErr = errors.New("task store is unavailable")
	} else {
		listed, diagnostics, err := app.Tasks.ListWithDiagnostics()
		if err != nil {
			tasksErr = err
		} else {
			tasks = tasksForRepo(listed, repository)
			for _, diagnostic := range diagnostics {
				tasksErr = errors.Join(tasksErr, diagnostic)
			}
		}
	}

	rt := app.Runtime()
	sessions, runtimeErr := rt.List(ctx)
	local := localRepoContext{
		Context: inventory.CollectRepoContextWithOptions(ctx, repository, tasks, inventory.RepoContextOptions{
			Runtime: rt.Name(), Sessions: sessions, RuntimeObserved: runtimeErr == nil,
			RuntimeErr: runtimeErr, IncludeActivity: includeTopology,
		}),
		Runtimes: []repocontext.RuntimeInput{{
			Backend: rt.Name(), Available: rt.Available(), Sessions: sessions, Err: runtimeErr,
		}},
		SelectedCheckout: -1,
	}
	local.Context.TaskErr = tasksErr
	local.Context.RuntimeErr = runtimeErr
	if selected, ok := local.Context.CheckoutIndexForPath(selectedPath); ok {
		local.SelectedCheckout = selected
	} else {
		local.SelectionErr = fmt.Errorf("selected path %s is not represented by the repository worktree inventory", config.Contract(selectedPath))
	}
	if includeTopology && repository.HasGit {
		local.Topology, local.TopologyErr = gitx.RecoveryTopologyOf(ctx, repository.Path)
	}
	return local
}

func resolveRepoContextTarget(ctx context.Context, app *App, args []string) (repo.Repo, string, error) {
	if len(args) == 1 {
		resolved, _, err := resolveRepoRef(app, args[0])
		if err != nil {
			return repo.Repo{}, "", err
		}
		if !resolved.HasGit {
			return resolved, resolved.Path, nil
		}
		discovered, err := gitx.Discover(ctx, resolved.Path)
		if err != nil {
			return repo.Repo{}, "", err
		}
		selectedPath := discovered.Root
		if selectedPath == "" {
			selectedPath = discovered.MainRoot
		}
		mainPath := discovered.MainRoot
		if mainPath == "" {
			mainPath = selectedPath
		}
		name := resolved.Name
		if name == "" {
			name = discovered.Name
		}
		return repo.Repo{
			Name: name, Path: mainPath, RealPath: mainPath,
			GitDir: discovered.GitCommonDir, CommonDir: discovered.GitCommonDir,
			MainRoot: mainPath, HasGit: true, Bare: discovered.Bare,
		}, selectedPath, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return repo.Repo{}, "", err
	}
	discovered, err := gitx.Discover(ctx, cwd)
	if err != nil {
		return repo.Repo{}, "", fmt.Errorf("%s is not a git repository — pass a repo", config.Contract(cwd))
	}
	selectedPath := discovered.Root
	if selectedPath == "" {
		selectedPath = discovered.MainRoot
	}
	return repo.Repo{
		Name: discovered.Name, Path: discovered.MainRoot, RealPath: discovered.MainRoot,
		GitDir: discovered.GitCommonDir, CommonDir: discovered.GitCommonDir,
		MainRoot: discovered.MainRoot, HasGit: true, Bare: discovered.Bare,
	}, selectedPath, nil
}

func collectRepoContextForge(ctx context.Context, app *App, refresh bool, now time.Time) repocontext.ForgeInput {
	cacheInput := func(cache forge.Cache, collectionErr error) repocontext.ForgeInput {
		freshness := assessment.FreshnessFresh
		if forgeCacheStale(cache, app.Cfg.Forge.CacheTTL.Duration, now) {
			freshness = assessment.FreshnessStale
		}
		completeness := assessment.CompletenessComplete
		if !cache.Complete || collectionErr != nil {
			completeness = assessment.CompletenessPartial
		}
		return repocontext.ForgeInput{
			Authority: assessment.AuthorityCache, Freshness: freshness,
			Completeness: completeness, ObservedAt: cache.FetchedAt,
			Records: append([]forge.RemoteRepo{}, cache.Repos...),
			Err:     errors.Join(collectionErr, forgeCacheErrors(cache)),
		}
	}

	if !refresh {
		cache, ok := forge.LoadCacheAny(remoteCachePath())
		if !ok {
			return repocontext.ForgeInput{Err: errors.New("no cached forge inventory; rerun with --refresh to collect one")}
		}
		return cacheInput(cache, nil)
	}

	rows, collectionErr := collectRemotes(ctx, app)
	records := make([]forge.RemoteRepo, 0, len(rows))
	for _, row := range rows {
		records = append(records, row.Repo)
	}
	if collectionErr == nil {
		return repocontext.ForgeInput{
			Authority: assessment.AuthorityRemoteLive, Freshness: assessment.FreshnessFresh,
			Completeness: assessment.CompletenessComplete, ObservedAt: now,
			Records: records,
		}
	}
	if cache, ok := forge.LoadCacheAny(remoteCachePath()); ok {
		cached := cacheInput(cache, collectionErr)
		if len(records) > 0 {
			cached.Records = records
		}
		return cached
	}
	if len(records) > 0 {
		return repocontext.ForgeInput{
			Authority: assessment.AuthorityRemoteLive, Freshness: assessment.FreshnessFresh,
			Completeness: assessment.CompletenessPartial, ObservedAt: now,
			Records: records, Err: collectionErr,
		}
	}
	return repocontext.ForgeInput{Err: collectionErr}
}

func forgeCacheStale(cache forge.Cache, ttl time.Duration, now time.Time) bool {
	if cache.FetchedAt.IsZero() {
		return true
	}
	if ttl <= 0 {
		return false
	}
	return now.Sub(cache.FetchedAt) > ttl
}

func forgeCacheErrors(cache forge.Cache) error {
	var providerErrors []error
	kinds := make([]string, 0, len(cache.Providers))
	byKind := map[string]forge.ProviderStatus{}
	for kind, status := range cache.Providers {
		name := string(kind)
		kinds = append(kinds, name)
		byKind[name] = status
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		status := byKind[kind]
		if status.Error != "" {
			providerErrors = append(providerErrors, fmt.Errorf("%s: %s", kind, status.Error))
		} else if !status.Complete {
			providerErrors = append(providerErrors, fmt.Errorf("%s: provider inventory is incomplete", kind))
		}
	}
	return errors.Join(providerErrors...)
}

func collectRepoContextFleet(ctx context.Context, app *App, refresh bool) repocontext.FleetInput {
	cfg, err := loadFleetConfig(app)
	if err != nil {
		return repocontext.FleetInput{Err: err}
	}
	input := repocontext.FleetInput{
		Configured: true, CacheTTL: cfg.Defaults.CacheTTL.Duration,
		ConfiguredHostNames: make([]string, len(cfg.Hosts)),
		Results:             make([]fleet.HostResult, len(cfg.Hosts)),
	}
	for index, host := range cfg.Hosts {
		input.ConfiguredHostNames[index] = host.Name
	}
	if len(cfg.Hosts) == 0 {
		return input
	}
	parallel := cfg.Defaults.MaxParallel
	if parallel <= 0 {
		parallel = 1
	}
	semaphore := make(chan struct{}, parallel)
	var wait sync.WaitGroup
	for index, host := range cfg.Hosts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			input.Results[index] = collectFleetHost(ctx, host, !refresh)
		}()
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		input.Err = err
	}
	return input
}
