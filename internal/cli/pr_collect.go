package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

// prScope selects which provider surface to ask.
type prScope string

const (
	scopeAccount prScope = "account"
	scopeLocal   prScope = "local"
	scopeAll     prScope = "all"
)

// prLocalGit is present only when the request's head branch is actually checked
// out and its status was read successfully. Keeping it optional prevents a
// missing/cold checkout from looking clean through Go zero values.
type prLocalGit struct {
	Dirty    bool   `json:"dirty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Upstream string `json:"upstream,omitempty"`
}

// prLocal is the local task/checkout associated with a request. Association by
// task intent can exist even when the branch is cold or its worktree vanished;
// BranchCheckedOut distinguishes that from a live checkout match.
type prLocal struct {
	TaskID             string      `json:"task_id,omitempty"`
	TaskState          string      `json:"task_state,omitempty"`
	RepoPath           string      `json:"repo_path,omitempty"`
	Checkout           string      `json:"checkout,omitempty"`
	ExpectedBranch     string      `json:"expected_branch,omitempty"`
	LiveBranch         string      `json:"live_branch,omitempty"`
	BranchCheckedOut   bool        `json:"branch_checked_out"`
	CheckoutExists     bool        `json:"checkout_exists"`
	WorktreeRegistered bool        `json:"worktree_registered"`
	StatusAvailable    bool        `json:"status_available"`
	StatusError        string      `json:"status_error,omitempty"`
	Git                *prLocalGit `json:"git,omitempty"`
}

type prRow struct {
	PR    forge.PullRequest
	Local *prLocal
}

// prRepoSelector is normalized once at flag parsing. Unknown means a bare
// owner/name which should be tried against every ready provider; a URL or a
// provider-qualified value pins the provider.
type prRepoSelector struct {
	Kind forge.Kind
	Host string
	Name string
}

type prCollectOptions struct {
	Scope    prScope
	Query    forge.PRQuery
	Repos    []prRepoSelector
	AllRepos bool
}

// prCollection keeps effective options beside results so JSON and prompts can
// never report the requested scope after the collector had to narrow it.
type prCollection struct {
	Rows      []prRow
	Providers []prProviderStatus
	Effective prCollectOptions
	Err       error
}

type prProviderStatus struct {
	Forge  forge.Kind `json:"forge"`
	Status string     `json:"status"`
	Detail string     `json:"detail,omitempty"`
	Action string     `json:"action,omitempty"`
}

func normalizePRSelectors(values []string) ([]prRepoSelector, error) {
	selectors := make([]prRepoSelector, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, errors.New("--repo must not be empty")
		}
		identity := forge.ParseRemoteIdentity(value)
		kind, host, name := identity.Kind, identity.Host, identity.Name
		if kind == forge.Unknown {
			for _, candidate := range []forge.Kind{forge.GitHub, forge.GitLab} {
				prefix := string(candidate) + ":"
				if strings.HasPrefix(strings.ToLower(value), prefix) {
					kind, name = candidate, strings.TrimSpace(value[len(prefix):])
					break
				}
			}
		}
		if name == "" {
			name = strings.TrimSuffix(strings.Trim(value, "/"), ".git")
		}
		if strings.Count(name, "/") < 1 || strings.ContainsRune(name, '\x00') {
			return nil, fmt.Errorf("invalid --repo %q: want owner/name, provider:owner/name, or a forge URL", raw)
		}
		key := string(kind) + "/" + strings.ToLower(host) + "/" + strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		selectors = append(selectors, prRepoSelector{Kind: kind, Host: host, Name: name})
	}
	return selectors, nil
}

func collectPullRequests(ctx context.Context, app *App, requested prCollectOptions) prCollection {
	effective := requested
	if effective.Scope == "" {
		effective.Scope = scopeAll
	}
	// Account search cannot distinguish merged from closed. When it cannot
	// contribute, report the surface which actually ran rather than preserving a
	// misleading requested scope.
	if effective.Query.EffectiveState() != forge.PRStateOpen &&
		(effective.Scope == scopeAccount || effective.Scope == scopeAll) {
		effective.Scope = scopeLocal
	}

	providers := configuredForges(app)
	var statuses []prProviderStatus
	var ready []forge.Forge
	for _, provider := range providers {
		if _, ok := provider.(forge.PRLister); !ok {
			if provider.Kind() == forge.AzureDevOps {
				statuses = append(statuses, prProviderStatus{
					Forge: provider.Kind(), Status: "unsupported",
					Detail: "dev cannot list Azure DevOps pull requests yet",
				})
			}
			continue
		}
		readiness := forge.ProbeForge(ctx, provider)
		if !readiness.Ready() {
			detail := ""
			if readiness.Status == forge.ReadinessProbeFailed {
				detail = compactDetail(readiness.Detail)
			}
			statuses = append(statuses, prProviderStatus{
				Forge: provider.Kind(), Status: string(readiness.Status),
				Detail: detail, Action: readiness.Action,
			})
			continue
		}
		statuses = append(statuses, prProviderStatus{Forge: provider.Kind(), Status: string(readiness.Status)})
		ready = append(ready, provider)
	}
	if len(ready) == 0 {
		return prCollection{Providers: statuses, Effective: effective,
			Err: errors.New("no authenticated forge CLI: " + remediation(statuses))}
	}

	var targets []prRepoTarget
	if effective.Scope == scopeLocal || effective.Scope == scopeAll {
		targets = prRepoTargets(ctx, app, effective)
	}

	var (
		mu       sync.Mutex
		errs     []error
		byKey    = map[string]forge.PullRequest{}
		wg       sync.WaitGroup
		requests = make(chan struct{}, 4)
	)
	absorb := func(prs []forge.PullRequest) {
		for _, pr := range prs {
			if !matchesPRSelectors(pr, effective.Repos) {
				continue
			}
			key := pr.Key()
			existing, ok := byKey[key]
			if !ok {
				byKey[key] = pr
				continue
			}
			if existing.Detail == forge.PRDetailSummary && pr.Detail == forge.PRDetailFull {
				byKey[key] = forge.MergePR(existing, pr)
			} else {
				byKey[key] = forge.MergePR(pr, existing)
			}
		}
	}

	for _, provider := range ready {
		if effective.Scope == scopeAccount {
			for _, selector := range effective.Repos {
				if selector.Kind == provider.Kind() && selector.Host != "" &&
					!strings.EqualFold(selector.Host, forge.ConfiguredHost(provider.Kind())) {
					mu.Lock()
					errs = append(errs, fmt.Errorf("%s remote %s/%s does not match configured host %s; set the provider host explicitly before querying it",
						provider.Kind(), selector.Host, selector.Name, forge.ConfiguredHost(provider.Kind())))
					mu.Unlock()
				}
			}
		}
		if effective.Scope == scopeAccount || effective.Scope == scopeAll {
			wg.Add(1)
			go func(f forge.Forge) {
				defer wg.Done()
				prs, err := forge.ListAccountPRs(ctx, f, effective.Query)
				mu.Lock()
				defer mu.Unlock()
				absorb(prs)
				if err != nil && !isUnsupported(err) {
					errs = append(errs, err)
				}
			}(provider)
		}
		if effective.Scope == scopeLocal || effective.Scope == scopeAll {
			for _, target := range targets {
				if target.Kind != forge.Unknown && target.Kind != provider.Kind() {
					continue
				}
				if target.Host != "" && !strings.EqualFold(target.Host, forge.ConfiguredHost(provider.Kind())) {
					mu.Lock()
					errs = append(errs, fmt.Errorf("%s remote %s/%s does not match configured host %s; set the provider host explicitly before querying it",
						provider.Kind(), target.Host, target.Name, forge.ConfiguredHost(provider.Kind())))
					mu.Unlock()
					continue
				}
				wg.Add(1)
				go func(f forge.Forge, name string) {
					defer wg.Done()
					requests <- struct{}{}
					defer func() { <-requests }()
					prs, err := forge.ListRepoPRs(ctx, f, name, effective.Query)
					mu.Lock()
					defer mu.Unlock()
					absorb(prs)
					if err != nil && !isUnsupported(err) {
						errs = append(errs, err)
					}
				}(provider, target.Name)
			}
		}
	}
	wg.Wait()

	prs := make([]forge.PullRequest, 0, len(byKey))
	for _, pr := range byKey {
		prs = append(prs, pr)
	}
	rows := joinLocalCheckouts(ctx, app, prs)
	sortPRRows(rows)
	return prCollection{Rows: rows, Providers: statuses, Effective: effective, Err: errors.Join(errs...)}
}

func matchesPRSelectors(pr forge.PullRequest, selectors []prRepoSelector) bool {
	if len(selectors) == 0 {
		return true
	}
	for _, selector := range selectors {
		if selector.Kind != forge.Unknown && selector.Kind != pr.Forge {
			continue
		}
		if selector.Host != "" && !strings.EqualFold(selector.Host, pr.Host) {
			continue
		}
		if strings.EqualFold(selector.Name, pr.Repo) {
			return true
		}
	}
	return false
}

func remediation(statuses []prProviderStatus) string {
	var parts []string
	for _, status := range statuses {
		if status.Action != "" {
			parts = append(parts, status.Action)
		}
	}
	if len(parts) == 0 {
		return "install and sign in to gh or glab"
	}
	return strings.Join(parts, "; ")
}

func isUnsupported(err error) bool {
	var unsupported *forge.ErrUnsupported
	return errors.As(err, &unsupported)
}

type prRepoTarget struct {
	Kind forge.Kind
	Host string
	Name string
}

func prRepoTargets(ctx context.Context, app *App, opts prCollectOptions) []prRepoTarget {
	if len(opts.Repos) > 0 {
		targets := make([]prRepoTarget, 0, len(opts.Repos))
		for _, selector := range opts.Repos {
			targets = append(targets, prRepoTarget(selector))
		}
		return targets
	}

	seen := map[string]bool{}
	var targets []prRepoTarget
	addRemote := func(remoteURL string) {
		identity := forge.ParseRemoteIdentity(remoteURL)
		if identity.Kind == forge.Unknown || identity.Name == "" {
			return
		}
		key := string(identity.Kind) + "/" + strings.ToLower(identity.Host) + "/" + strings.ToLower(identity.Name)
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, prRepoTarget{Kind: identity.Kind, Host: identity.Host, Name: identity.Name})
	}

	if !opts.AllRepos {
		// Tasks are the engaged-repository authority. Resolve their Git common
		// directories directly so a repository outside scan_roots, or reached
		// through a symlink index, is not lost by literal path comparison.
		if tasks, err := app.Tasks.List(); err == nil {
			seenRepos := map[string]bool{}
			for _, task := range tasks {
				if task.RepoPath == "" {
					continue
				}
				repository, err := gitx.Discover(ctx, task.RepoPath)
				if err != nil || seenRepos[repository.GitCommonDir] {
					continue
				}
				seenRepos[repository.GitCommonDir] = true
				addRemote(gitx.RemoteFromConfig(repository.GitCommonDir, "origin"))
			}
		}
		return targets
	}

	locals, _ := repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
	for _, repository := range locals {
		if repository.Bare {
			continue
		}
		// Querying the remote does not require choosing a local clone. Multiple
		// clones of one origin collapse to one provider call; the separate local
		// join still fails closed when checkout evidence is ambiguous.
		addRemote(gitx.RemoteFromConfig(repository.CommonDir, "origin"))
	}
	return targets
}

func joinLocalCheckouts(ctx context.Context, app *App, prs []forge.PullRequest) []prRow {
	rows := make([]prRow, 0, len(prs))
	wanted := map[string]bool{}
	for _, pr := range prs {
		if pr.HeadBranch == "" {
			continue
		}
		headRepo := pr.HeadRepo
		if headRepo == "" && !pr.CrossRepository {
			headRepo = pr.Repo
		}
		if headRepo != "" {
			wanted[string(pr.Forge)+"/"+strings.ToLower(pr.Host)+"/"+strings.ToLower(headRepo)] = true
		}
	}
	if len(wanted) == 0 {
		for _, pr := range prs {
			rows = append(rows, prRow{PR: pr})
		}
		return rows
	}

	tasks, err := app.Tasks.List()
	if err != nil {
		app.warnf("could not read tasks for the local join: %v", err)
		tasks = nil
	}
	inventoryRows := inventory.Collect(ctx, tasks, app.Runtime(), inventory.Options{SkipRuntime: true})

	type joinKey struct{ repo, branch string }
	live := map[joinKey]*prLocal{}
	expected := map[joinKey]*prLocal{}
	liveAmbiguous := map[joinKey]bool{}
	expectedAmbiguous := map[joinKey]bool{}
	add := func(values map[joinKey]*prLocal, ambiguous map[joinKey]bool, key joinKey, local *prLocal) {
		if key.repo == "" || key.branch == "" || ambiguous[key] {
			return
		}
		if existing := values[key]; existing != nil {
			if samePRLocalPath(existing.Checkout, local.Checkout) {
				if existing.TaskID == "" && local.TaskID != "" {
					values[key] = local
				}
				return
			}
			delete(values, key)
			ambiguous[key] = true
			return
		}
		values[key] = local
	}

	identities := map[string]string{}
	for _, row := range inventoryRows {
		if row.Task == nil || row.Task.RepoPath == "" || row.Task.Branch == "" {
			continue
		}
		identity, resolved := identities[row.Task.RepoPath]
		if !resolved {
			remote := forge.ParseRemoteIdentity(gitx.Remote(ctx, row.Task.RepoPath, "origin"))
			if remote.Kind != forge.Unknown && remote.Name != "" {
				identity = string(remote.Kind) + "/" + strings.ToLower(remote.Host) + "/" + strings.ToLower(remote.Name)
			}
			identities[row.Task.RepoPath] = identity
		}
		if identity == "" {
			continue
		}

		statusAvailable := row.CheckoutExists && row.StatusErr == nil
		worktreeRegistered := row.Task.WorktreePath == "" || !row.WorktreeMissing
		local := &prLocal{
			TaskID: row.Task.ID, TaskState: string(row.Task.State),
			RepoPath: row.Task.RepoPath, Checkout: row.Checkout,
			ExpectedBranch: row.Task.Branch, LiveBranch: row.Status.Branch,
			CheckoutExists: row.CheckoutExists, WorktreeRegistered: worktreeRegistered,
			StatusAvailable: statusAvailable,
		}
		if row.StatusErr != nil {
			local.StatusError = boundedError(row.StatusErr)
		} else if !row.CheckoutExists {
			local.StatusError = "checkout path is missing"
		} else if !worktreeRegistered {
			local.StatusError = "worktree registration is missing"
		}
		local.BranchCheckedOut = statusAvailable && worktreeRegistered && row.Status.Branch == row.Task.Branch
		if local.BranchCheckedOut {
			local.Git = &prLocalGit{Dirty: row.Status.Dirty(), Ahead: row.Status.Ahead, Behind: row.Status.Behind, Upstream: row.Status.Upstream}
			add(live, liveAmbiguous, joinKey{repo: identity, branch: row.Status.Branch}, local)
		}
		add(expected, expectedAmbiguous, joinKey{repo: identity, branch: row.Task.Branch}, local)
	}

	// A real Git checkout does not need a persisted task to be locally relevant.
	// Discover only repositories whose remote identity occurs in this PR batch,
	// then add canonical, external, adopted, and unmanaged worktrees.
	localRepos, _ := repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
	if discovered, err := gitx.Discover(ctx, currentDirectory()); err == nil {
		localRepos = append(localRepos, repo.Repo{Path: discovered.MainRoot, CommonDir: discovered.GitCommonDir, HasGit: true})
	}
	seenCommonDirs := map[string]bool{}
	for _, task := range tasks {
		if task.RepoPath == "" {
			continue
		}
		if discovered, err := gitx.Discover(ctx, task.RepoPath); err == nil && !seenCommonDirs[discovered.GitCommonDir] {
			seenCommonDirs[discovered.GitCommonDir] = true
			localRepos = append(localRepos, repo.Repo{Path: discovered.MainRoot, CommonDir: discovered.GitCommonDir, HasGit: true})
		}
	}
	seenCommonDirs = map[string]bool{}
	for _, repository := range localRepos {
		if repository.CommonDir == "" || seenCommonDirs[repository.CommonDir] {
			continue
		}
		seenCommonDirs[repository.CommonDir] = true
		remote := forge.ParseRemoteIdentity(gitx.RemoteFromConfig(repository.CommonDir, "origin"))
		identity := string(remote.Kind) + "/" + strings.ToLower(remote.Host) + "/" + strings.ToLower(remote.Name)
		if remote.Kind == forge.Unknown || !wanted[identity] {
			continue
		}
		worktrees, err := gitx.Worktrees(ctx, repository.Path)
		if err != nil {
			continue
		}
		for _, worktree := range worktrees {
			checkout := worktree.Path
			_, statErr := os.Stat(checkout)
			exists := statErr == nil
			status, statusErr := gitx.StatusOf(ctx, checkout)
			statusAvailable := exists && statusErr == nil && !worktree.Prunable
			branch := worktree.Branch
			if branch == "" && statusAvailable {
				branch = status.Branch
			}
			local := &prLocal{
				RepoPath: repository.Path, Checkout: checkout,
				ExpectedBranch: branch, LiveBranch: status.Branch,
				CheckoutExists: exists, WorktreeRegistered: !worktree.Prunable,
				StatusAvailable: statusAvailable,
			}
			if statusErr != nil {
				local.StatusError = boundedError(statusErr)
			} else if !exists {
				local.StatusError = "checkout path is missing"
			} else if worktree.Prunable {
				local.StatusError = "worktree registration is prunable"
			}
			local.BranchCheckedOut = statusAvailable && branch != "" && status.Branch == branch
			if local.BranchCheckedOut {
				local.Git = &prLocalGit{Dirty: status.Dirty(), Ahead: status.Ahead, Behind: status.Behind, Upstream: status.Upstream}
				add(live, liveAmbiguous, joinKey{repo: identity, branch: branch}, local)
			}
			add(expected, expectedAmbiguous, joinKey{repo: identity, branch: branch}, local)
		}
	}

	for _, pr := range prs {
		row := prRow{PR: pr}
		if pr.HeadBranch != "" {
			headRepo := pr.HeadRepo
			if headRepo == "" {
				if pr.CrossRepository {
					rows = append(rows, row)
					continue
				}
				headRepo = pr.Repo
			}
			key := joinKey{repo: string(pr.Forge) + "/" + strings.ToLower(pr.Host) + "/" + strings.ToLower(headRepo), branch: pr.HeadBranch}
			if local := live[key]; local != nil {
				row.Local = local
			} else if local := expected[key]; local != nil {
				row.Local = local
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func samePRLocalPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	canonicalA, errA := pathx.Canonical(a)
	canonicalB, errB := pathx.Canonical(b)
	if errA == nil && errB == nil {
		return canonicalA == canonicalB
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func boundedError(err error) string {
	const limit = 300
	message := strings.TrimSpace(err.Error())
	if len(message) > limit {
		return message[:limit] + "…"
	}
	return message
}

func sortPRRows(rows []prRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		iLocal, jLocal := rows[i].Local != nil, rows[j].Local != nil
		if iLocal != jLocal {
			return iLocal
		}
		if !rows[i].PR.UpdatedAt.Equal(rows[j].PR.UpdatedAt) {
			return rows[i].PR.UpdatedAt.After(rows[j].PR.UpdatedAt)
		}
		return rows[i].PR.Key() < rows[j].PR.Key()
	})
}

func compactDetail(detail string) string {
	const limit = 100
	for _, line := range strings.Split(detail, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "x✓X✗- \t")
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "ERROR") {
			continue
		}
		if len(line) > limit {
			line = line[:limit] + "…"
		}
		return line
	}
	return ""
}
