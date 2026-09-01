package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newFleetCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Inspect and safely synchronize dev repositories across machines",
		Long: `Fan out to machines in $XDG_CONFIG_HOME/dev/remotes.toml over SSH.

Each machine runs its own dev binary and therefore uses its own XDG config and
repository paths. Missing dev installations and repositories are reported but
do not make the rest of the fleet unusable.`,
	}
	cmd.AddCommand(
		newFleetListCmd(app),
		newFleetStatusCmd(app),
		newFleetSyncCmd(app),
		newFleetMachineIDCmd(app),
		newFleetFilesCmd(app),
		newFleetOpenCmd(app),
		newFleetConfigCmd(app),
		newFleetSnapshotCmd(app),
		newFleetApplySyncCmd(app),
		newFleetCapabilityProtocolCmd(app),
		newFleetFilesPlanProtocolCmd(app),
		newFleetFilesApplyProtocolCmd(app),
		newFleetOpenHelperCmd(app, "_open-herdr", true),
		newFleetOpenHelperCmd(app, "_shell", false),
	)
	return cmd
}

func encodeOpenRequest(request fleet.OpenRequest) string {
	data, _ := json.Marshal(request)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeOpenRequest(value string) (fleet.OpenRequest, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return fleet.OpenRequest{}, err
	}
	var request fleet.OpenRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return fleet.OpenRequest{}, err
	}
	return request, nil
}

func newFleetOpenHelperCmd(app *App, use string, herdrOpen bool) *cobra.Command {
	var encoded string
	cmd := &cobra.Command{
		Use:    use,
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			request, err := decodeOpenRequest(encoded)
			if err != nil {
				return fmt.Errorf("decode fleet open request: %w", err)
			}
			repository, err := resolveFleetOpenRepository(ctxOf(), app, request)
			if err != nil {
				return err
			}
			if herdrOpen {
				rt, ok := app.runtimeNamed("herdr").(*runtime.Herdr)
				if !ok || !rt.Available() {
					return errors.New("remote Herdr server is not available")
				}
				opened, err := openCheckout(ctxOf(), rt, repository.Path, repository.Name)
				if err != nil {
					return err
				}
				if err := rt.Focus(ctxOf(), opened.Handle); err != nil {
					return err
				}
				return json.NewEncoder(app.Out).Encode(map[string]string{"workspace": opened.Handle, "path": repository.Path})
			}
			if err := os.Chdir(repository.Path); err != nil {
				return err
			}
			return replaceProcessWithShell()
		},
	}
	cmd.Flags().StringVar(&encoded, "request", "", "encoded request")
	_ = cmd.Flags().MarkHidden("request")
	return cmd
}

func resolveFleetOpenRepository(ctx context.Context, app *App, request fleet.OpenRequest) (repo.Repo, error) {
	repositories, err := repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
	if err != nil {
		return repo.Repo{}, err
	}
	var matches []repo.Repo
	wantedIdentity := catalog.NormalizeRemoteIdentity(request.RemoteIdentity)
	for _, repository := range repositories {
		if request.Path != "" && (sameCleanPath(request.Path, repository.Path) || sameCleanPath(request.Path, repository.RealPath)) {
			return repository, nil
		}
		if wantedIdentity == "" {
			continue
		}
		topology, topologyErr := gitx.RecoveryTopologyOf(ctx, repository.Path)
		if topologyErr == nil && matchingSnapshotIdentity(topology, wantedIdentity) {
			matches = append(matches, repository)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return repo.Repo{}, fmt.Errorf("remote identity matches multiple repositories")
	}
	return repo.Repo{}, fmt.Errorf("repository %q is no longer discoverable", request.Name)
}

func matchingSnapshotIdentity(topology gitx.RecoveryTopology, identity string) bool {
	for _, remote := range topology.Remotes {
		for _, raw := range append(append([]string{}, remote.FetchURLs...), remote.PushURLs...) {
			if catalog.NormalizeRemoteIdentity(raw) == identity {
				return true
			}
		}
	}
	return false
}

func newFleetOpenCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "open <host> <repo>",
		Short: "Open a remote repository through Herdr or an SSH login shell",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadFleetConfig(app)
			if err != nil {
				return err
			}
			var host *fleet.Host
			for index := range cfg.Hosts {
				if cfg.Hosts[index].Name == args[0] {
					host = &cfg.Hosts[index]
					break
				}
			}
			if host == nil {
				return fmt.Errorf("unknown fleet host %q", args[0])
			}
			live := collectFleetHost(ctxOf(), *host, false)
			if live.State != fleet.HostOK || live.Snapshot == nil {
				return fmt.Errorf("host %s is not live: %s %s", host.Name, live.State, live.Error)
			}
			repository, err := selectFleetRepository(live.Snapshot.Repositories, args[1])
			if err != nil {
				return err
			}
			request := fleet.OpenRequest{Name: repository.Display, Path: repository.Path}
			if len(repository.RemoteIdentities) > 0 {
				request.RemoteIdentity = repository.RemoteIdentities[0]
			}
			encoded := encodeOpenRequest(request)
			transport := fleet.Transport{Err: app.Err}
			if live.Snapshot.Runtime == "herdr" && host.SSHAlias != "" && host.PasswordKind() == "none" {
				prepared := transport.Run(ctxOf(), *host, []string{"fleet", "_open-herdr", "--request", encoded}, nil, false)
				if prepared.ExitCode == 0 {
					process := exec.CommandContext(ctxOf(), "herdr", "--remote", host.SSHAlias)
					process.Stdin, process.Stdout, process.Stderr = os.Stdin, os.Stdout, os.Stderr
					if err := process.Run(); err == nil {
						return nil
					} else {
						app.warnf("Herdr remote attach failed; falling back to SSH: %v", err)
					}
				}
			}
			return transport.Interactive(ctxOf(), *host, []string{"fleet", "_shell", "--request", encoded}, live.PasswordAuth)
		},
	}
}

func selectFleetRepository(repositories []fleet.RepoSnapshot, query string) (fleet.RepoSnapshot, error) {
	for _, repository := range repositories {
		if query == repository.Name || query == repository.Display || query == repository.Path {
			return repository, nil
		}
		for _, identity := range repository.RemoteIdentities {
			if query == identity {
				return repository, nil
			}
		}
	}
	needle := strings.ToLower(query)
	var matches []fleet.RepoSnapshot
	for _, repository := range repositories {
		haystack := strings.ToLower(strings.Join([]string{repository.Name, repository.Display, repository.Path, strings.Join(repository.RemoteIdentities, " ")}, " "))
		if strings.Contains(haystack, needle) {
			matches = append(matches, repository)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return fleet.RepoSnapshot{}, fmt.Errorf("remote repository %q not found", query)
	default:
		return fleet.RepoSnapshot{}, fmt.Errorf("remote repository %q is ambiguous", query)
	}
}

func newFleetApplySyncCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:    "_sync",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var request fleet.SyncRequest
			if err := json.NewDecoder(app.In).Decode(&request); err != nil {
				return fmt.Errorf("decode fleet sync request: %w", err)
			}
			result := fleet.ApplySync(ctxOf(), app.Cfg, request)
			return json.NewEncoder(app.Out).Encode(result)
		},
	}
}

func newFleetSyncCmd(app *App) *cobra.Command {
	var hostNames []string
	var push, jsonOut bool
	var remoteName string
	cmd := &cobra.Command{
		Use:   "sync <repo>",
		Short: "Push optionally, then safely fast-forward clean matching checkouts",
		Long: `Synchronize one branch through its Git remote.

The source checkout must be clean. Without --push, its HEAD must already equal
the fetched upstream. Targets fetch the matching remote and fast-forward only a
clean checkout of the same branch. Different branches are never switched;
ahead, dirty, divergent and ambiguous clones are reported without modification.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request, source, err := prepareFleetSync(ctxOf(), app, args[0], remoteName, push)
			if err != nil {
				return err
			}
			cfg, err := loadFleetConfig(app)
			if err != nil {
				return err
			}
			selected := map[string]bool{}
			for _, name := range hostNames {
				selected[name] = true
			}
			hosts := make([]fleet.Host, 0, len(cfg.Hosts))
			found := map[string]bool{}
			for _, host := range cfg.Hosts {
				found[host.Name] = true
				if len(selected) == 0 || selected[host.Name] {
					hosts = append(hosts, host)
				}
			}
			for name := range selected {
				if !found[name] {
					return fmt.Errorf("unknown fleet host %q", name)
				}
			}
			results := syncFleetHosts(ctxOf(), hosts, cfg.Defaults.MaxParallel, request)
			if jsonOut {
				payload := struct {
					Source  fleet.SyncResult   `json:"source"`
					Targets []fleet.SyncResult `json:"targets"`
				}{Source: source, Targets: results}
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(payload); err != nil {
					return err
				}
			} else {
				renderFleetSync(app, source, results)
			}
			for _, result := range results {
				if !result.Success() {
					return errors.New("fleet sync could not verify every managed checkout")
				}
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&push, "push", false, "push the source branch before fan-out")
	flags.StringVar(&remoteName, "remote", "", "Git remote to publish/check (default: upstream, then origin)")
	flags.StringSliceVar(&hostNames, "host", nil, "only these configured host names")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON results")
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
}

func prepareFleetSync(ctx context.Context, app *App, ref, requestedRemote string, push bool) (fleet.SyncRequest, fleet.SyncResult, error) {
	repository, _, err := repo.Resolve(ctx, app.Cfg.DiscoveryRoots(), ref)
	if err != nil {
		return fleet.SyncRequest{}, fleet.SyncResult{}, err
	}
	if repository.Bare {
		return fleet.SyncRequest{}, fleet.SyncResult{}, fmt.Errorf("%s is bare and has no checkout to publish", repository.Display())
	}
	status, err := gitx.StatusOf(ctx, repository.Path)
	if err != nil {
		return fleet.SyncRequest{}, fleet.SyncResult{}, err
	}
	if status.Detached || status.Branch == "" {
		return fleet.SyncRequest{}, fleet.SyncResult{}, errors.New("fleet sync requires an attached branch")
	}
	if status.Dirty() || status.Conflicted > 0 {
		return fleet.SyncRequest{}, fleet.SyncResult{}, fmt.Errorf("source checkout must be clean (%s)", status.Summary())
	}
	remote := requestedRemote
	if remote == "" && status.Upstream != "" {
		remote, _, _ = strings.Cut(status.Upstream, "/")
	}
	if remote == "" {
		remote = "origin"
	}
	remoteURL := gitx.Remote(ctx, repository.Path, remote)
	identity := catalog.NormalizeRemoteIdentity(remoteURL)
	if identity == "" {
		return fleet.SyncRequest{}, fleet.SyncResult{}, fmt.Errorf("remote %q has no cross-host identity", remote)
	}
	if _, err := gitx.Run(ctx, repository.Path, "fetch", "--prune", "--quiet", remote); err != nil {
		return fleet.SyncRequest{}, fleet.SyncResult{}, err
	}
	if push {
		if _, err := gitx.Run(ctx, repository.Path, "push", "--set-upstream", remote, status.Branch); err != nil {
			return fleet.SyncRequest{}, fleet.SyncResult{}, fmt.Errorf("push %s: %w", status.Branch, err)
		}
	} else {
		remoteOID, err := gitx.Run(ctx, repository.Path, "rev-parse", "--verify", remote+"/"+status.Branch+"^{commit}")
		if err != nil {
			return fleet.SyncRequest{}, fleet.SyncResult{}, fmt.Errorf("fetched %s/%s is unavailable", remote, status.Branch)
		}
		head, err := gitx.Run(ctx, repository.Path, "rev-parse", "HEAD")
		if err != nil {
			return fleet.SyncRequest{}, fleet.SyncResult{}, err
		}
		if head != remoteOID {
			return fleet.SyncRequest{}, fleet.SyncResult{}, fmt.Errorf("source HEAD does not equal %s/%s after fetch; push or reconcile it first", remote, status.Branch)
		}
	}
	head, err := gitx.Run(ctx, repository.Path, "rev-parse", "HEAD")
	if err != nil {
		return fleet.SyncRequest{}, fleet.SyncResult{}, err
	}
	request := fleet.SyncRequest{RemoteIdentity: identity, Branch: status.Branch, ExpectedOID: head}
	source := fleet.SyncResult{
		Host: config.Hostname(), State: fleet.SyncCurrent, Repo: repository.Display(), Path: repository.Path,
		Branch: status.Branch, Remote: remote, BeforeOID: head, AfterOID: head, ExpectedOID: head,
	}
	return request, source, nil
}

func syncFleetHosts(ctx context.Context, hosts []fleet.Host, maxParallel int, request fleet.SyncRequest) []fleet.SyncResult {
	if maxParallel <= 0 {
		maxParallel = 4
	}
	results := make([]fleet.SyncResult, len(hosts))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	requestJSON, _ := json.Marshal(request)
	for index, host := range hosts {
		wg.Add(1)
		go func(index int, host fleet.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			transport := fleet.Transport{Err: os.Stderr}
			response := transport.Run(ctx, host, []string{"fleet", "_sync"}, requestJSON, false)
			result := fleet.SyncResult{Host: host.Name, Branch: request.Branch, ExpectedOID: request.ExpectedOID}
			switch {
			case response.ExitCode == 0:
				if err := json.Unmarshal(response.Stdout, &result); err != nil {
					result.State, result.Error = fleet.SyncFailed, "remote dev returned invalid sync JSON"
				}
				result.Host = host.Name
			case response.TimedOut || response.ExitCode == 124:
				result.State, result.Error = fleet.SyncTimeout, strings.TrimSpace(string(response.Stderr))
			case response.ExitCode == 127:
				result.State = fleet.SyncNoDev
			case response.ExitCode == 255:
				result.State, result.Error = fleet.SyncUnreachable, strings.TrimSpace(string(response.Stderr))
			default:
				result.State, result.Error = fleet.SyncIncompatible, strings.TrimSpace(string(response.Stderr))
			}
			results[index] = result
		}(index, host)
	}
	wg.Wait()
	return results
}

func renderFleetSync(app *App, source fleet.SyncResult, targets []fleet.SyncResult) {
	style := app.outStyle()
	table := app.newTable("HOST", "STATE", "REPO", "BRANCH", "PATH", "DETAIL")
	table.Add(source.Host, style.hostState(string(source.State)), source.Repo, source.Branch, config.Contract(source.Path), style.dim("source"))
	for _, result := range targets {
		table.Add(result.Host, style.hostState(string(result.State)), dash(result.Repo), dash(result.Branch),
			config.Contract(result.Path), style.warning(truncate(result.Error, 64)))
	}
	table.Render(app.Out)
}

func loadFleetConfig(app *App) (fleet.Config, error) {
	cfg, err := fleet.LoadConfig(app.remotesPath)
	if err != nil {
		return cfg, err
	}
	cfg.ApplyDefaults()
	if cfg.Source != "" {
		if err := fleet.CheckPrivateMode(cfg.Source, cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func localFleetSnapshot(ctx context.Context, app *App) (fleet.Snapshot, error) {
	rt := app.Runtime()
	rows, err := collectReposWithOptions(ctx, app, rt, repoCollectOptions{})
	if err != nil {
		return fleet.Snapshot{}, err
	}
	repositories := make([]fleet.RepoSnapshot, 0, len(rows))
	for _, row := range rows {
		counts := fleet.TaskCounts{}
		for _, tracked := range row.Tasks {
			switch tracked.State {
			case task.Hot:
				counts.Hot++
			case task.Warm:
				counts.Warm++
			case task.Cold:
				counts.Cold++
			case task.Done:
				counts.Done++
			}
		}
		identities := remoteIdentities(row)
		repositories = append(repositories, fleet.RepoSnapshot{
			Name: row.Repo.Name, Display: row.Repo.Display(), Category: row.Repo.Category,
			Path: row.Repo.Path, RealPath: row.Repo.RealPath, RemoteIdentities: identities,
			Branch: row.Status.Branch, Status: row.Status, LastActivity: row.LastActivity,
			Worktrees: row.Worktrees, Tasks: counts, Live: row.Live, Runtime: row.Runtime,
			RuntimeHandle: row.RuntimeHandle, AgentStatus: row.RuntimeStatus, Topology: row.Topology,
		})
	}
	return fleet.Snapshot{
		SchemaVersion: fleet.SnapshotSchemaVersion,
		Host:          config.Hostname(),
		DevVersion:    versionFromBuild(),
		GeneratedAt:   time.Now().UTC(),
		Runtime:       rt.Name(),
		Repositories:  repositories,
	}, nil
}

func remoteIdentities(row tui.RepoRow) []string {
	seen := map[string]bool{}
	var identities []string
	for _, remote := range row.Topology.Remotes {
		for _, raw := range append(append([]string{}, remote.FetchURLs...), remote.PushURLs...) {
			identity := catalog.NormalizeRemoteIdentity(raw)
			if identity != "" && !seen[identity] {
				seen[identity] = true
				identities = append(identities, identity)
			}
		}
	}
	sort.Strings(identities)
	return identities
}

func newFleetSnapshotCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "_snapshot",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshot, err := localFleetSnapshot(ctxOf(), app)
			if err != nil {
				return err
			}
			return json.NewEncoder(app.Out).Encode(snapshot)
		},
	}
	return cmd
}

type fleetCollectOptions struct {
	CachedOnly bool
	Hosts      map[string]bool
}

func collectFleet(ctx context.Context, app *App, options fleetCollectOptions) ([]fleet.HostResult, fleet.Config, error) {
	cfg, err := loadFleetConfig(app)
	if err != nil {
		return nil, cfg, err
	}
	local, err := localFleetSnapshot(ctx, app)
	if err != nil {
		return nil, cfg, err
	}
	var results []fleet.HostResult
	if len(options.Hosts) == 0 || options.Hosts[local.Host] {
		results = append(results, fleet.HostResult{Name: local.Host, Local: true, State: fleet.HostOK, Snapshot: &local})
	}
	selected := make([]fleet.Host, 0, len(cfg.Hosts))
	found := map[string]bool{local.Host: true}
	for _, host := range cfg.Hosts {
		found[host.Name] = true
		if len(options.Hosts) > 0 && !options.Hosts[host.Name] {
			continue
		}
		selected = append(selected, host)
	}
	for name := range options.Hosts {
		if !found[name] {
			return nil, cfg, fmt.Errorf("unknown fleet host %q", name)
		}
	}
	remoteResults := make([]fleet.HostResult, len(selected))
	sem := make(chan struct{}, cfg.Defaults.MaxParallel)
	var wg sync.WaitGroup
	for index, host := range selected {
		wg.Add(1)
		go func(index int, host fleet.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			remoteResults[index] = collectFleetHost(ctx, host, options.CachedOnly)
		}(index, host)
	}
	wg.Wait()
	return append(results, remoteResults...), cfg, nil
}

func collectFleetHost(ctx context.Context, host fleet.Host, cachedOnly bool) fleet.HostResult {
	endpointID := fleet.EndpointID(host)
	cached, cachedAt, haveCache := fleet.LoadCache(host)
	if cachedOnly {
		if haveCache {
			return fleet.HostResult{Name: host.Name, State: fleet.HostStale, Snapshot: &cached, CachedAt: &cachedAt, FromCache: true, EndpointID: endpointID}
		}
		return fleet.HostResult{Name: host.Name, State: fleet.HostUnreachable, Error: "no cached snapshot", EndpointID: endpointID}
	}
	transport := fleet.Transport{Err: os.Stderr}
	result := transport.Run(ctx, host, []string{"fleet", "_snapshot"}, nil, false)
	if result.ExitCode == 0 {
		var snapshot fleet.Snapshot
		if err := json.Unmarshal(result.Stdout, &snapshot); err == nil && snapshot.SchemaVersion == fleet.SnapshotSchemaVersion {
			_ = fleet.SaveCache(host, snapshot)
			return fleet.HostResult{Name: host.Name, State: fleet.HostOK, Snapshot: &snapshot, PasswordAuth: result.UsedPassword, EndpointID: endpointID}
		}
		return cachedFleetFailure(host, fleet.HostInvalid, "remote dev returned invalid snapshot JSON", cached, cachedAt, haveCache)
	}
	state := fleet.HostIncompatible
	detail := strings.TrimSpace(string(result.Stderr))
	switch {
	case result.TimedOut || result.ExitCode == 124:
		state = fleet.HostTimeout
	case result.ExitCode == 127:
		state = fleet.HostNoDev
	case result.ExitCode == 255:
		state = fleet.HostUnreachable
	case strings.Contains(strings.ToLower(detail), "unknown command"):
		state = fleet.HostIncompatible
	}
	if detail == "" {
		detail = fmt.Sprintf("remote command exited %d", result.ExitCode)
	}
	return cachedFleetFailure(host, state, detail, cached, cachedAt, haveCache)
}

func cachedFleetFailure(host fleet.Host, state fleet.HostState, detail string, cached fleet.Snapshot, cachedAt time.Time, haveCache bool) fleet.HostResult {
	if haveCache && state != fleet.HostNoDev {
		return fleet.HostResult{Name: host.Name, State: fleet.HostStale, Snapshot: &cached, CachedAt: &cachedAt, FromCache: true, Error: detail, EndpointID: fleet.EndpointID(host)}
	}
	return fleet.HostResult{Name: host.Name, State: state, Error: detail, EndpointID: fleet.EndpointID(host)}
}

func newFleetListCmd(app *App) *cobra.Command {
	var hostNames []string
	var repoQuery string
	var jsonOut, cachedOnly, strict bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List repositories and activity across this machine and configured hosts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			selected := map[string]bool{}
			for _, name := range hostNames {
				selected[name] = true
			}
			results, _, err := collectFleet(ctxOf(), app, fleetCollectOptions{CachedOnly: cachedOnly, Hosts: selected})
			if err != nil {
				return err
			}
			if jsonOut {
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(results); err != nil {
					return err
				}
			} else {
				renderFleetList(app, results, repoQuery)
			}
			if strict && anyStrictFleetFailure(results) {
				return errors.New("one or more fleet hosts could not be verified")
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringSliceVar(&hostNames, "host", nil, "only these configured host names")
	flags.StringVar(&repoQuery, "repo", "", "filter repository name, identity, branch or path")
	flags.BoolVar(&jsonOut, "json", false, "emit versioned JSON")
	flags.BoolVar(&cachedOnly, "cached", false, "do not contact remote hosts")
	flags.BoolVar(&strict, "strict", false, "fail when a host is unreachable or incompatible")
	return cmd
}

// fleetLive keeps the placeholder neutral: an em dash means "no runtime here",
// which is not a success worth painting green.
func fleetLive(style cliStyle, live string) string {
	if live == "" {
		return "—"
	}
	return style.success(live)
}

func renderFleetList(app *App, results []fleet.HostResult, query string) {
	style := app.outStyle()
	table := app.newTable("HOST", "STATE", "REPO", "BRANCH", "GIT", "LIVE", "TASKS", "LATEST", "PATH")
	needle := strings.ToLower(strings.TrimSpace(query))
	for _, result := range results {
		if result.Snapshot == nil || len(result.Snapshot.Repositories) == 0 {
			table.Add(result.Name, style.hostState(string(result.State)), "—", "—", "—", "—", "—", "—",
				style.warning(truncate(result.Error, 42)))
			continue
		}
		for _, repository := range result.Snapshot.Repositories {
			haystack := strings.ToLower(strings.Join([]string{repository.Name, repository.Display, repository.Branch, repository.Path, strings.Join(repository.RemoteIdentities, " ")}, " "))
			if needle != "" && !strings.Contains(haystack, needle) {
				continue
			}
			live := ""
			if repository.Live {
				live = repository.Runtime
				if repository.AgentStatus != "" {
					live += ":" + repository.AgentStatus
				}
			}
			latest := "—"
			if !repository.LastActivity.IsZero() {
				latest = humanAge(time.Since(repository.LastActivity))
			}
			table.Add(result.Name, style.hostState(string(result.State)), truncate(repository.Display, 28), truncate(repository.Branch, 22),
				style.git(repository.Status.Summary()), fleetLive(style, live), fleetTaskSummary(repository.Tasks),
				style.dim(latest), config.Contract(repository.Path))
		}
	}
	table.Render(app.Out)
}

func fleetRows(results []fleet.HostResult) []tui.FleetRow {
	var rows []tui.FleetRow
	for _, result := range results {
		if result.Snapshot == nil || len(result.Snapshot.Repositories) == 0 {
			rows = append(rows, tui.FleetRow{Host: result.Name, Local: result.Local, State: result.State, Error: result.Error, FromCache: result.FromCache})
			continue
		}
		for index := range result.Snapshot.Repositories {
			repository := result.Snapshot.Repositories[index]
			rows = append(rows, tui.FleetRow{
				Host: result.Name, Local: result.Local, State: result.State, Repository: &repository,
				Error: result.Error, FromCache: result.FromCache,
			})
		}
	}
	return rows
}

func cachedFleetRows(app *App) []tui.FleetRow {
	cfg, err := loadFleetConfig(app)
	if err != nil {
		return nil
	}
	var results []fleet.HostResult
	for _, host := range cfg.Hosts {
		snapshot, cachedAt, ok := fleet.LoadCache(host)
		if !ok || (cfg.Defaults.CacheTTL.Duration > 0 && time.Since(cachedAt) > cfg.Defaults.CacheTTL.Duration) {
			continue
		}
		results = append(results, fleet.HostResult{
			Name: host.Name, State: fleet.HostStale, Snapshot: &snapshot,
			CachedAt: &cachedAt, FromCache: true,
		})
	}
	return fleetRows(results)
}

func fleetTaskSummary(counts fleet.TaskCounts) string {
	var parts []string
	for _, item := range []struct {
		icon string
		n    int
	}{{"🔥", counts.Hot}, {"🌤", counts.Warm}, {"❄", counts.Cold}, {"✓", counts.Done}} {
		if item.n > 0 {
			parts = append(parts, fmt.Sprintf("%s%d", item.icon, item.n))
		}
	}
	return dash(strings.Join(parts, " "))
}

func newFleetStatusCmd(app *App) *cobra.Command {
	var jsonOut, strict bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Probe configured hosts and report snapshot health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			results, cfg, err := collectFleet(ctxOf(), app, fleetCollectOptions{})
			if err != nil {
				return err
			}
			if jsonOut {
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(results); err != nil {
					return err
				}
			} else {
				style := app.outStyle()
				table := app.newTable("HOST", "STATE", "REPOS", "SNAPSHOT", "DETAIL")
				for _, result := range results {
					repos, age := "—", "—"
					if result.Snapshot != nil {
						repos = fmt.Sprintf("%d", len(result.Snapshot.Repositories))
						age = humanAge(time.Since(result.Snapshot.GeneratedAt))
					}
					table.Add(result.Name, style.hostState(string(result.State)), repos, style.dim(age),
						style.warning(truncate(result.Error, 72)))
				}
				table.Render(app.Out)
				if len(cfg.Hosts) == 0 {
					fmt.Fprintf(app.Out, "\nNo remote hosts configured; edit %s\n", config.Contract(fleetConfigPath(app)))
				}
			}
			if strict && anyStrictFleetFailure(results) {
				return errors.New("one or more fleet hosts could not be verified")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail when a host is unreachable or incompatible")
	return cmd
}

func anyStrictFleetFailure(results []fleet.HostResult) bool {
	for _, result := range results {
		if result.StrictFailure() {
			return true
		}
	}
	return false
}

func newFleetConfigCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Show, edit, initialise and locate remotes.toml"}
	cmd.AddCommand(
		&cobra.Command{Use: "path", Short: "Print the remotes config path", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			path := app.remotesPath
			if path == "" {
				path = fleet.ConfigFile()
			}
			fmt.Fprintln(app.Out, config.Expand(path))
			return nil
		}},
		&cobra.Command{Use: "show", Short: "Print the effective remotes configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadFleetConfig(app)
			if err != nil {
				return err
			}
			for index := range cfg.Hosts {
				if cfg.Hosts[index].PasswordKind() == "plain" {
					cfg.Hosts[index].SSHLoginPasswordSource.Value = "(redacted)"
				}
			}
			return toml.NewEncoder(app.Out).Encode(cfg)
		}},
		newFleetConfigInitCmd(app),
		newFleetConfigEditCmd(app),
	)
	return cmd
}

const fleetStarterConfig = `# dev remote fleet configuration.
schema_version = 1

[defaults]
connect_timeout = "15s"
command_timeout = "5m"
cache_ttl = "15m"
max_parallel = 4
dev_path = "auto"

# Prefer an alias from ~/.ssh/config so ProxyJump, IdentityAgent and host-key
# policy behave exactly like an ordinary ssh invocation.
# [[hosts]]
# name = "lab"
# ssh_alias = "lab"
# Run dev fleet machine-id lab, verify the UUID independently, then pin it
# before any mutating fleet operation:
# machine_id = "00000000-0000-4000-8000-000000000000"

# Explicit connection fields are also supported. Password login is retried only
# after key/agent authentication is rejected.
# [[hosts]]
# name = "vps"
# hostname = "203.0.113.10"
# user = "dev"
# port = 22
# identity_file = "~/.ssh/id_ed25519"
# ssh_login_password_source = { type = "bitwarden", item = "ssh-vps-login" }
`

func fleetConfigPath(app *App) string {
	if app.remotesPath != "" {
		return config.Expand(app.remotesPath)
	}
	return fleet.ConfigFile()
}

func newFleetConfigInitCmd(app *App) *cobra.Command {
	var force, stdout bool
	cmd := &cobra.Command{Use: "init", Short: "Write a starter remotes.toml", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if stdout {
			fmt.Fprint(app.Out, fleetStarterConfig)
			return nil
		}
		path := fleetConfigPath(app)
		if _, err := os.Stat(path); err == nil && !force {
			return fmt.Errorf("%s already exists (use --force to overwrite)", config.Contract(path))
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(fleetStarterConfig), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(app.Out, "wrote %s\n", config.Contract(path))
		return nil
	}}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite an existing file")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "print without writing")
	return cmd
}

// fleetConfigEditorProcess builds the editor invocation for remotes.toml,
// seeding the starter file when there is none. The TUI runs the same command
// under tea.ExecProcess, so the two entry points cannot drift.
func fleetConfigEditorProcess(app *App, editor string) (*exec.Cmd, error) {
	path := fleetConfigPath(app)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		// 0600: remotes.toml may carry a plaintext SSH password.
		if err := os.WriteFile(path, []byte(fleetStarterConfig), 0o600); err != nil {
			return nil, err
		}
	}
	chosen, err := resolveEditor(editor)
	if err != nil {
		return nil, err
	}
	process := exec.Command(shellPath(), "-c", chosen+" "+shellQuote(path))
	process.Stdin, process.Stdout, process.Stderr = os.Stdin, os.Stdout, os.Stderr
	return process, nil
}

func newFleetConfigEditCmd(app *App) *cobra.Command {
	var editor string
	cmd := &cobra.Command{Use: "edit", Short: "Open remotes.toml in $VISUAL or $EDITOR", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		process, err := fleetConfigEditorProcess(app, editor)
		if err != nil {
			return err
		}
		return process.Run()
	}}
	cmd.Flags().StringVar(&editor, "editor", "", "editor command override")
	return cmd
}
