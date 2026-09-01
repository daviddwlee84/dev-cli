package cli

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

// prScope selects which provider surface to ask.
type prScope string

const (
	// scopeAccount is the cheap whole-account search: two calls total, no
	// matter how many repositories exist.
	scopeAccount prScope = "account"
	// scopeLocal asks per repository, which is the only surface that reports
	// a head branch and therefore the only one that can be joined to a
	// worktree.
	scopeLocal prScope = "local"
	// scopeAll is both, with local rows upgrading account rows.
	scopeAll prScope = "all"
)

// prLocal is the local checkout a request corresponds to, when there is one.
type prLocal struct {
	TaskID    string `json:"task_id,omitempty"`
	TaskState string `json:"task_state,omitempty"`
	RepoPath  string `json:"repo_path,omitempty"`
	Checkout  string `json:"checkout,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Dirty     bool   `json:"dirty"`
	Ahead     int    `json:"ahead,omitempty"`
	Behind    int    `json:"behind,omitempty"`
}

// prRow is one request plus whatever local context dev could attach.
type prRow struct {
	PR    forge.PullRequest
	Local *prLocal
}

// prCollectOptions narrows a scan.
type prCollectOptions struct {
	Scope    prScope
	Query    forge.PRQuery
	Repos    []string
	AllRepos bool
}

// prProviderStatus reports what one provider contributed, so that a signed-out
// GitLab is visible as a gap rather than as silently missing rows.
type prProviderStatus struct {
	Forge  forge.Kind `json:"forge"`
	Status string     `json:"status"`
	Detail string     `json:"detail,omitempty"`
	Action string     `json:"action,omitempty"`
}

// collectPullRequests gathers requests from every ready provider.
//
// Readiness is probed before querying rather than after failing: a per-
// repository scan fans out, and one sign-out would otherwise produce the same
// error once per repository.
func collectPullRequests(ctx context.Context, app *App, opts prCollectOptions) ([]prRow, []prProviderStatus, error) {
	providers := configuredForges(app)
	var statuses []prProviderStatus
	var ready []forge.Forge
	for _, provider := range providers {
		if _, ok := provider.(forge.PRLister); !ok {
			// Azure DevOps declines by not implementing the capability. Report
			// it only when the user configured it, so the default listing is
			// not noisy about something they never asked for.
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
			// A signed-out provider needs no diagnostic: the status word says
			// what is wrong and the action says what to do. The provider's own
			// multi-line `auth status` report is only worth showing when the
			// probe failed for some reason dev cannot name.
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
		return nil, statuses, errors.New("no authenticated forge CLI: " + remediation(statuses))
	}

	scope := opts.Scope
	if scope == "" {
		scope = scopeAll
	}
	// Only the per-repository surface can answer a non-open state, so asking
	// for merged requests implies it rather than quietly returning nothing.
	if opts.Query.EffectiveState() != forge.PRStateOpen && scope == scopeAccount {
		scope = scopeLocal
	}

	var targets []prRepoTarget
	if scope == scopeLocal || scope == scopeAll {
		targets = prRepoTargets(ctx, app, opts)
	}

	var (
		mu    sync.Mutex
		errs  []error
		byKey = map[string]forge.PullRequest{}
		order []string
		wg    sync.WaitGroup
		// Forge APIs rate-limit on their own budget, so the per-repository
		// fan-out gets its own small bound rather than sharing the local
		// enrichment limiter.
		requests = make(chan struct{}, 4)
	)
	absorb := func(prs []forge.PullRequest) {
		for _, pr := range prs {
			key := pr.Key()
			existing, ok := byKey[key]
			if !ok {
				byKey[key] = pr
				order = append(order, key)
				continue
			}
			// The richer row wins; roles only the account surface knows are
			// carried across.
			if existing.Detail == forge.PRDetailSummary && pr.Detail == forge.PRDetailFull {
				byKey[key] = forge.MergePR(existing, pr)
			} else {
				byKey[key] = forge.MergePR(pr, existing)
			}
		}
	}

	for _, provider := range ready {
		if scope == scopeAccount || scope == scopeAll {
			wg.Add(1)
			go func(f forge.Forge) {
				defer wg.Done()
				prs, err := forge.ListAccountPRs(ctx, f, opts.Query)
				mu.Lock()
				defer mu.Unlock()
				absorb(prs)
				if err != nil && !isUnsupported(err) {
					errs = append(errs, err)
				}
			}(provider)
		}
		if scope == scopeLocal || scope == scopeAll {
			for _, target := range targets {
				if target.Kind != provider.Kind() {
					continue
				}
				wg.Add(1)
				go func(f forge.Forge, name string) {
					defer wg.Done()
					requests <- struct{}{}
					defer func() { <-requests }()
					prs, err := forge.ListRepoPRs(ctx, f, name, opts.Query)
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

	prs := make([]forge.PullRequest, 0, len(order))
	for _, key := range order {
		prs = append(prs, byKey[key])
	}
	rows := joinLocalCheckouts(ctx, app, prs)
	sortPRRows(rows)
	return rows, statuses, errors.Join(errs...)
}

// remediation renders the actions of every provider that is not ready, so the
// "nothing to show" case tells the user what to do about it.
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

// prRepoTarget is one repository to query, in the provider's own naming.
type prRepoTarget struct {
	Kind forge.Kind
	Name string
}

// prRepoTargets picks which repositories the per-repository surface should ask
// about.
//
// By default that is only repositories dev is engaged with — ones carrying a
// task or a managed worktree. Querying every discovered repository would mean
// one subprocess per repository under paths.scan_roots, which on a populated
// machine is dozens of calls for rows the user did not ask about. --all-repos
// opts into the wide scan.
func prRepoTargets(ctx context.Context, app *App, opts prCollectOptions) []prRepoTarget {
	if len(opts.Repos) > 0 {
		targets := make([]prRepoTarget, 0, len(opts.Repos))
		for _, name := range opts.Repos {
			kind := forge.GitHub
			if parsed, identity := forge.IdentityFromURL(name); parsed != forge.Unknown && identity != "" {
				kind = parsed
			}
			targets = append(targets, prRepoTarget{Kind: kind, Name: name})
		}
		return targets
	}

	engaged := map[string]bool{}
	if !opts.AllRepos {
		tasks, err := app.Tasks.List()
		if err == nil {
			for _, t := range tasks {
				if t.RepoPath != "" {
					engaged[t.RepoPath] = true
				}
			}
		}
	}

	locals, _ := repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
	seen := map[string]bool{}
	ambiguous := map[string]bool{}
	byKey := map[string]prRepoTarget{}
	var order []string
	for _, r := range locals {
		if r.Bare {
			continue
		}
		if !opts.AllRepos && !engaged[r.Path] {
			continue
		}
		kind, name := forge.IdentityFromURL(gitx.RemoteFromConfig(r.CommonDir, "origin"))
		if kind == forge.Unknown || name == "" {
			continue
		}
		key := string(kind) + "/" + strings.ToLower(name)
		// Two clones of the same remote cannot be told apart, so drop the
		// entry rather than guess which one a request belongs to. This mirrors
		// how matchRemoteLocals resolves the same ambiguity.
		if ambiguous[key] {
			continue
		}
		if seen[key] {
			ambiguous[key] = true
			delete(byKey, key)
			continue
		}
		seen[key] = true
		byKey[key] = prRepoTarget{Kind: kind, Name: name}
		order = append(order, key)
	}
	targets := make([]prRepoTarget, 0, len(order))
	for _, key := range order {
		if target, ok := byKey[key]; ok {
			targets = append(targets, target)
		}
	}
	return targets
}

// joinLocalCheckouts attaches the local task or worktree a request corresponds
// to. The join is by head branch, which only the per-repository surface
// reports, so account-only rows stay unjoined by construction.
//
// This runs here rather than inside inventory.Collect deliberately: that
// collector is on the hot path of `dev ls` and the TUI, and must not acquire a
// dependency on a forge round-trip.
func joinLocalCheckouts(ctx context.Context, app *App, prs []forge.PullRequest) []prRow {
	rows := make([]prRow, 0, len(prs))
	needsJoin := false
	for _, pr := range prs {
		if pr.HeadBranch != "" {
			needsJoin = true
			break
		}
	}
	if !needsJoin {
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

	// Key on repository identity plus branch: the same branch name in two
	// repositories is common and must not cross-match.
	type joinKey struct{ repo, branch string }
	locals := map[joinKey]*prLocal{}
	// Several tasks usually share a repository, and resolving its remote costs
	// a git process, so resolve each repository once.
	identities := map[string]string{}
	for _, row := range inventoryRows {
		if row.Task == nil {
			continue
		}
		branch := row.Task.Branch
		if branch == "" {
			branch = row.Status.Branch
		}
		if branch == "" || row.Task.RepoPath == "" {
			continue
		}
		identity, resolved := identities[row.Task.RepoPath]
		if !resolved {
			kind, name := forge.IdentityFromURL(gitx.Remote(ctx, row.Task.RepoPath, "origin"))
			if kind != forge.Unknown && name != "" {
				identity = string(kind) + "/" + strings.ToLower(name)
			}
			identities[row.Task.RepoPath] = identity
		}
		if identity == "" {
			continue
		}
		key := joinKey{repo: identity, branch: branch}
		if _, exists := locals[key]; exists {
			continue
		}
		locals[key] = &prLocal{
			TaskID: row.Task.ID, TaskState: string(row.Task.State),
			RepoPath: row.Task.RepoPath, Checkout: row.Checkout,
			Branch: branch, Dirty: row.Status.Dirty(),
			Ahead: row.Status.Ahead, Behind: row.Status.Behind,
		}
	}

	for _, pr := range prs {
		row := prRow{PR: pr}
		if pr.HeadBranch != "" {
			key := joinKey{repo: string(pr.Forge) + "/" + strings.ToLower(pr.Repo), branch: pr.HeadBranch}
			if local, ok := locals[key]; ok {
				row.Local = local
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// sortPRRows puts the rows that need attention first: locally checked out,
// then most recently updated.
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

// compactDetail reduces a provider's diagnostic output to one short line.
//
// `gh auth status` and `glab auth status` print a whole report — endpoints,
// token fingerprints, banners — and pasting that into a status line is the
// same unreadable wall this feature exists to remove. The remediation is the
// useful part, and it travels separately.
func compactDetail(detail string) string {
	const limit = 100
	for _, line := range strings.Split(detail, "\n") {
		line = strings.TrimSpace(line)
		// Skip the provider's own success ticks and decoration.
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
