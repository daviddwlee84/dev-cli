package triage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

type Options struct {
	ScopeLabel string
	// A non-nil Selection disables root discovery, including an empty selection.
	Selection []Target
	Snapshots []RepositorySnapshot
	Roots     []string
	Kind      string
	All       bool
	StaleDays int
}
type Config struct {
	TryHooks  experiment.Hooks
	Config    config.Config
	Tasks     *task.Store
	Catalog   *catalog.Store
	Runtimes  []runtime.Runtime
	Host      string
	CWD       string
	Lifecycle taskflow.LifecycleConfig
}
type Service struct {
	cfg   Config
	Store Store
}

func New(cfg Config) *Service {
	return &Service{cfg: cfg, Store: Store{Dir: filepath.Join(cfg.Config.StateDir(), "triage")}}
}

type runtimeSnapshot struct {
	backend  string
	sessions []runtime.Session
	err      error
}
type asset struct {
	id, kind, phase string
	name, note      string
	tags            []string
	history         bool
	pending         bool
}

func (s *Service) Collect(ctx context.Context, opts Options) (Report, error) {
	if opts.StaleDays <= 0 {
		opts.StaleDays = 14
	}
	r := Report{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Complete: true, Sources: []Source{}, Items: []Item{}, RemoteEvidence: "local remote-tracking refs; fetch explicitly to refresh"}
	addSource := func(name string, complete bool, detail string) {
		r.Sources = append(r.Sources, Source{Name: name, Complete: complete, Detail: detail})
		if !complete {
			r.Complete = false
		}
	}
	seeds := map[string]bool{}
	assets := map[string]asset{}
	plain := map[string]bool{}
	canonical := func(p string) string {
		v, e := pathx.Canonical(p)
		if e == nil {
			return v
		}
		v, _ = filepath.Abs(p)
		return v
	}
	scoped := opts.Selection != nil
	for _, snapshot := range opts.Snapshots {
		r.Sources = append(r.Sources, Source{Name: "dashboard:" + snapshot.Repo.CommonDir, ObservedAt: snapshot.ObservedAt, Complete: snapshot.TopologyErr == nil && snapshot.Context.IdentityErr == nil && snapshot.Context.TaskErr == nil && snapshot.Context.WorktreeErr == nil, Detail: "in-memory metadata snapshot; action plans revalidate live authority"})
	}
	selectedPaths := map[string]Target{}
	for _, target := range opts.Selection {
		selectedPaths[canonical(target.Path)] = target
	}
	entries, diagnostics, err := s.cfg.Catalog.ListWithDiagnostics()
	catalogOK := err == nil && len(diagnostics) == 0
	addSource("catalog", catalogOK, "catalog records with errors cannot authorize cleanup")
	for _, e := range entries {
		l, ok := e.LocationFor(s.cfg.Host)
		if !ok || l.CurrentPath == "" {
			continue
		}
		p := canonical(l.CurrentPath)
		a := asset{id: e.ID, kind: "repo", history: l.State != catalog.LocationPresent, pending: e.MoveIntent != nil, name: e.Title(), note: e.Note, tags: e.Tags}
		if e.Experiment != nil {
			a.phase = string(e.Experiment.Phase)
		}
		if e.Kind == catalog.KindTry && a.phase != string(catalog.PhaseGraduated) {
			a.kind = "try"
		}
		if _, exists := assets[p]; exists {
			a.pending = true
			catalogOK = false
		}
		assets[p] = a
		_, selected := selectedPaths[p]
		if (!scoped || selected) && (opts.All || !a.history) {
			seeds[p] = true
			if a.kind == "try" {
				plain[p] = true
			}
		}
	}
	// Observe immediate Try directories without calling List/Reconcile: listing
	// must neither create catalog entries nor reconcile interrupted moves.
	triesRoot := config.Expand(s.cfg.Config.Paths.TriesRoot)
	if !scoped {
		if ds, e := os.ReadDir(triesRoot); e == nil {
			for _, d := range ds {
				if strings.HasPrefix(d.Name(), ".") || !d.IsDir() {
					continue
				}
				p := canonical(filepath.Join(triesRoot, d.Name()))
				if _, ok := assets[p]; !ok {
					assets[p] = asset{kind: "try", phase: "active"}
				}
				seeds[p] = true
				plain[p] = true
			}
		} else if !os.IsNotExist(e) {
			addSource("tries", false, "Try root could not be read")
		}
		roots := append(append([]string{}, s.cfg.Config.DiscoveryRoots()...), opts.Roots...)
		for _, root := range roots {
			root = config.Expand(root)
			scanOptions := repo.DefaultOptions()
			scanComplete := true
			scanOptions.OnError = func(string, error) { scanComplete = false }
			found, e := repo.Discover(ctx, []string{root}, scanOptions)
			_, statErr := os.Stat(root)
			addSource(root, e == nil && statErr == nil && scanComplete, "discovery root unavailable or incomplete")
			for _, entry := range found {
				seeds[entry.Path] = true
			}
		}
		if s.cfg.CWD != "" {
			if g, e := gitx.Discover(ctx, s.cfg.CWD); e == nil {
				seeds[g.Root] = true
			}
		}
	} else {
		for p, target := range selectedPaths {
			if target.Kind == "try" {
				if target.CatalogID != "" && assets[p].id != target.CatalogID {
					delete(seeds, p)
					delete(plain, p)
					if assets[p].id != "" {
						addSource(p, false, "selected Try catalog identity changed; refresh selection")
					}
					continue
				}
				if target.CatalogID == "" {
					if _, err := os.Lstat(p); os.IsNotExist(err) {
						continue
					}
					if _, ok := assets[p]; !ok {
						assets[p] = asset{kind: "try", phase: "active"}
					}
				}
				plain[p] = true
			}
			seeds[p] = true
		}
	}
	tasks, td, te := s.cfg.Tasks.ListWithDiagnostics()
	tasksOK := te == nil && len(td) == 0
	addSource("tasks", tasksOK, "task records with errors cannot authorize cleanup")
	for _, t := range tasks {
		if scoped {
			continue
		}
		if t.RepoPath != "" {
			seeds[t.RepoPath] = true
		}
		if t.WorktreePath != "" {
			seeds[t.WorktreePath] = true
		}
	}
	runtimes := []runtimeSnapshot{}
	runtimeOK := len(s.cfg.Runtimes) > 0
	for _, rt := range s.cfg.Runtimes {
		sessions, e := rt.List(ctx)
		runtimes = append(runtimes, runtimeSnapshot{rt.Name(), sessions, e})
		ok := e == nil && rt.Name() != "none"
		addSource("runtime:"+rt.Name(), ok, "runtime coverage unavailable")
		runtimeOK = runtimeOK && ok
		for _, session := range sessions {
			if scoped {
				continue
			}
			for _, p := range session.Dirs {
				if g, e := gitx.Discover(ctx, p); e == nil {
					seeds[g.Root] = true
				}
			}
			if session.WorkspaceCheckout != "" {
				seeds[session.WorkspaceCheckout] = true
			}
		}
	}
	if len(s.cfg.Runtimes) == 0 {
		addSource("runtime", false, "runtime inspection disabled or unavailable")
	}
	repositories := map[string]gitx.Repo{}
	paths := make([]string, 0, len(seeds))
	for p := range seeds {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if e := ctx.Err(); e != nil {
			return r, e
		}
		g, e := gitx.Discover(ctx, path)
		if e == nil {
			common, ce := pathx.Canonical(g.GitCommonDir)
			if ce == nil {
				if target, ok := selectedPaths[canonical(path)]; ok && target.RepositoryID != "" && target.RepositoryID != common {
					addSource(path, false, "selected repository identity changed; select it again")
					continue
				}
				g.GitCommonDir = common
				repositories[common] = g
				continue
			}
		}
		p := canonical(path)
		a := assets[p]
		_, markerErr := os.Lstat(filepath.Join(p, ".git"))
		if plain[p] || a.id != "" || markerErr == nil || scoped {
			i := Item{ID: digest([]string{"directory", p}), RepositoryID: p, RepositoryPath: p, Path: p, Name: filepath.Base(p), Kind: a.kind, Scope: "directory", CatalogID: a.id, Phase: a.phase, History: a.history, Complete: false, Findings: []Finding{}, Actions: []Action{}}
			if i.Kind == "" {
				i.Kind = "repo"
			}
			if info, e := os.Stat(p); e == nil && info.IsDir() {
				directoryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				i.LastActivity, i.ContentsFingerprint, e = directoryFacts(directoryCtx, p)
				cancel()
				i.Complete = e == nil && catalogOK
				if e != nil {
					i.add("directory-incomplete", "Directory observation was incomplete", 0, false)
				}
				if markerErr == nil {
					i.Complete = false
					i.add("git-observation-error", "Git marker exists but repository discovery failed", 0, false)
				} else {
					i.add("non-git", "Local directory: inspect and preserve or archive individually", 1, true)
				}
			} else {
				i.add("unavailable", "Recorded directory is unavailable", 0, false)
			}
			i.Fingerprint = digest([]any{p, i.LastActivity, i.Findings})
			s.decorateIntent(&i, r.GeneratedAt)
			r.Items = append(r.Items, i)
		}
	}
	keys := make([]string, 0, len(repositories))
	for key := range repositories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	groups := make([][]Item, len(keys))
	var wg sync.WaitGroup
	limit := make(chan struct{}, 8)
	for n, key := range keys {
		wg.Add(1)
		go func(n int, key string) {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-limit }()
			repoCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			groups[n] = s.collectRepo(repoCtx, repositories[key], tasks, assets, runtimes, tasksOK && catalogOK, runtimeOK, opts, r.GeneratedAt)
		}(n, key)
	}
	wg.Wait()
	for _, group := range groups {
		r.Items = append(r.Items, group...)
	}
	// Keep task-only records even if discovery cannot open their repository.
	seenTasks := map[string]bool{}
	for _, i := range r.Items {
		for _, t := range i.Tasks {
			seenTasks[t.ID] = true
		}
	}
	for _, t := range tasks {
		if scoped {
			_, repoSelected := selectedPaths[canonical(t.RepoPath)]
			_, checkoutSelected := selectedPaths[canonical(t.WorktreePath)]
			if !repoSelected && !checkoutSelected {
				continue
			}
		}
		if seenTasks[t.ID] {
			continue
		}
		i := Item{ID: digest([]string{"task", t.ID}), RepositoryID: t.RepoPath, RepositoryPath: t.RepoPath, Path: t.WorktreePath, Name: t.Title(), Kind: "repo", Scope: "task", Tasks: []*task.Task{t}, LastActivity: t.Updated, Complete: false, Actions: []Action{}, Findings: []Finding{}}
		i.add("task-unavailable", "Task has no available repository/branch checkout; inspect its intent", 0, false)
		r.Items = append(r.Items, i)
	}
	filtered := r.Items[:0]
	for _, i := range r.Items {
		if a, ok := assets[i.Path]; ok && a.id != "" {
			i.Name, i.Note, i.Tags = a.name, a.note, append([]string{}, a.tags...)
		}
		s.tryCandidates(ctx, &i)
		if !i.Complete {
			r.Complete = false
		}
		if opts.Kind != "" && opts.Kind != "all" && i.Kind != opts.Kind {
			continue
		}
		if i.History && !opts.All {
			continue
		}
		filtered = append(filtered, i)
	}
	r.Items = filtered
	for _, source := range r.Sources {
		if !source.Complete {
			r.Complete = false
		}
	}
	SortItems(r.Items, false)
	return r, ctx.Err()
}

func (s *Service) collectRepo(ctx context.Context, g gitx.Repo, tasks []*task.Task, assets map[string]asset, runtimes []runtimeSnapshot, metadataOK, runtimeOK bool, opts Options, now time.Time) []Item {
	base := Item{RepositoryID: g.GitCommonDir, RepositoryPath: g.MainRoot, Path: g.MainRoot, Name: g.Name, Kind: "repo", Scope: "repository", Complete: metadataOK, Actions: []Action{}, Findings: []Finding{}}
	if base.RepositoryPath == "" {
		base.RepositoryPath = g.Root
		base.Path = g.Root
	}
	base.ID = digest([]string{base.RepositoryID, "repository"})
	if a, ok := assets[base.Path]; ok {
		base.Kind, base.CatalogID, base.Phase, base.History = a.kind, a.id, a.phase, a.history
	}
	pref, pe := s.Store.Read(g.GitCommonDir)
	if pe != nil {
		base.Complete = false
		base.add("preferences-error", "Triage preferences need repair before cleanup", 0, false)
	}
	base.PreferenceFingerprint = digest(pref)
	base.DisposableDirs = append([]string{}, pref.DisposableDirs...)
	var seed *RepositorySnapshot
	for n := range opts.Snapshots {
		if opts.Snapshots[n].Repo.CommonDir == g.GitCommonDir {
			seed = &opts.Snapshots[n]
			break
		}
	}
	var top gitx.RecoveryTopology
	var topErr error
	if seed != nil && seed.TopologyErr == nil {
		top, topErr = seed.Topology, seed.TopologyErr
	} else {
		top, topErr = gitx.RecoveryTopologyOf(ctx, base.RepositoryPath)
	}
	branches, branchErr := gitx.BranchStates(ctx, base.RepositoryPath)
	worktrees, wtErr := gitx.Worktrees(ctx, base.RepositoryPath)
	if topErr != nil || branchErr != nil || wtErr != nil {
		base.Complete = false
		base.add("git-observation-error", "Git topology could not be completely observed", 0, false)
	}
	if topErr == nil && len(top.Remotes) == 0 {
		base.add("no-remote", "Repository has no configured remote", 2, true)
	}
	for _, remote := range top.Remotes {
		base.Actions = append(base.Actions, Action{Name: "fetch", Remote: remote.Name, Availability: "candidate"})
	}
	if refs, e := gitx.Run(ctx, base.RepositoryPath, "for-each-ref", "--format=%(refname)", "refs/stash", "refs/tags", "refs/notes"); e != nil {
		base.Complete = false
		base.add("refs-error", "Other local refs could not be inspected", 0, false)
	} else if refs != "" {
		base.add("local-refs", "Review stash/tags/notes before future whole-repository eviction: "+strings.ReplaceAll(refs, "\n", ", "), 3, strings.Contains(refs, "refs/stash"))
	}
	// Reuse the shared task/worktree binding rules, including moved paths and
	// ambiguous claims. Triage must not invent a second ownership policy.
	related := []*task.Task{}
	mainCanonical, _ := pathx.Canonical(base.RepositoryPath)
	for _, t := range tasks {
		p, _ := pathx.Canonical(t.RepoPath)
		belongs := p != "" && p == mainCanonical
		if !belongs && t.WorktreePath != "" {
			wp, _ := pathx.Canonical(t.WorktreePath)
			for _, w := range worktrees {
				if wp != "" && wp == w.Path {
					belongs = true
				}
			}
		}
		if belongs {
			related = append(related, t)
		}
	}
	var joined inventory.RepoContext
	if seed != nil && seed.Context.WorktreeErr == nil && sameSnapshotTasks(seed.Context, related, worktrees) {
		joined = seed.Context
	} else {
		joined = inventory.CollectRepoContextWithOptions(ctx, repo.Repo{Path: base.RepositoryPath, RealPath: base.RepositoryPath, MainRoot: base.RepositoryPath, CommonDir: g.GitCommonDir, HasGit: true, Bare: g.Bare}, related, inventory.RepoContextOptions{Limiter: inventory.NewLimiter(1)})
	}
	if joined.WorktreeErr != nil || joined.IdentityErr != nil {
		base.Complete = false
	}
	rows := []Item{}
	for _, w := range worktrees {
		if w.Bare {
			continue
		}
		i := base
		i.Findings = append([]Finding{}, base.Findings...)
		i.Actions = []Action{}
		i.Scope = "checkout"
		i.Path = w.Path
		i.Worktree = &w
		i.ID = digest([]string{g.GitCommonDir, "checkout", w.Path})
		for _, b := range branches {
			if b.Ref == "refs/heads/"+w.Branch {
				i.Branch = &b
				break
			}
		}
		if a, ok := assets[w.Path]; ok {
			i.Kind = a.kind
			i.CatalogID = a.id
			i.Phase = a.phase
			i.History = a.history
			if a.pending {
				i.Complete = false
				i.add("catalog-conflict", "Asset has ambiguous identity or a pending move", 0, false)
			}
		}
		contents, e := gitx.InspectTriageContents(ctx, w.Path, pref.DisposableDirs)
		if e != nil {
			i.Complete = false
			i.add("contents-error", "Checkout contents could not be completely inspected", 0, false)
		} else {
			i.Status = &contents.Status
			i.Ignored = contents.Ignored
			i.ContentsFingerprint = contents.Fingerprint
			i.LastActivity = contents.Status.LatestChange
			for _, p := range contents.Ignored {
				if p.Modified.After(i.LastActivity) {
					i.LastActivity = p.Modified
				}
			}
			if contents.Status.Dirty() {
				i.add("uncommitted", contents.Status.Breakdown(), 1, true)
			}
			if len(contents.Ignored) > 0 {
				i.add("ignored", fmt.Sprintf("%d ignored paths; undeclared paths block removal", len(contents.Ignored)), 4, false)
			}
			if len(contents.Nested) > 0 {
				i.add("nested-repository", "Nested repositories require individual preservation", 0, true)
			}
		}
		if op, busy, e := gitx.InProgress(w.Path); e != nil {
			i.Complete = false
			i.add("operation-unknown", "Git operation could not be inspected", 0, false)
		} else if busy {
			i.add("git-operation", op+" in progress", 0, true)
		}
		if w.Locked || w.Prunable || w.Detached {
			i.add("checkout-protected", "Locked, prunable, or detached checkout: inspect individually", 0, false)
		}
		if inventory.IsClaudeHarnessWorktree(base.RepositoryPath, w.Path) {
			i.add("harness-owned", "Harness owns checkout cleanup", 3, false)
		}
		if !runtimeOK {
			i.add("runtime-unknown", "Runtime coverage is incomplete; checkout mutation requires fresh evidence", 3, false)
		}
		for _, rs := range runtimes {
			for _, session := range rs.sessions {
				if session.Covers(w.Path) || session.WorkspaceCheckout == w.Path {
					agent := len(session.AgentSessions) > 0
					for _, p := range session.Panes {
						agent = agent || p.Agent != ""
					}
					i.Runtimes = append(i.Runtimes, RuntimeLink{rs.backend, session.Handle, session.Label, agent})
					if agent {
						i.add("agent-occupied", "Recognized agent occupies checkout, including idle/done agents", 3, false)
					}
				}
			}
		}
		for _, bound := range joined.Checkouts {
			if bound.ID == w.Path {
				i.Tasks = append([]*task.Task{}, bound.Tasks...)
				for _, reason := range bound.ConflictReasons {
					i.Complete = false
					i.add("ownership-conflict", reason.Detail, 0, false)
				}
				for _, reason := range bound.DriftReasons {
					i.add("task-drift", reason.Detail, 3, false)
				}
			}
		}
		for _, t := range i.Tasks {
			if t.Updated.After(i.LastActivity) {
				i.LastActivity = t.Updated
			}
			if t.State == task.Hot && runtimeOK && len(i.Runtimes) == 0 {
				i.add("task-drift", "HOT task has no live runtime session", 3, false)
			}
		}
		if len(i.Runtimes) > 0 && len(i.Tasks) == 0 {
			i.add("runtime-unclaimed", "Live workspace has no task record", 3, false)
		}
		if len(i.Tasks) > 1 {
			i.Complete = false
			i.add("task-conflict", "Multiple tasks claim this checkout", 0, false)
		}
		if i.Branch != nil {
			s.branchFindings(&i)
		} else {
			i.add("no-attached-branch", "Unborn or detached checkout needs individual attention", 2, true)
		}
		s.lifecycleCandidates(&i, runtimeOK && metadataOK && pe == nil)
		s.finishItem(&i, pref, now, opts.StaleDays)
		rows = append(rows, i)
	}
	for _, b := range branches {
		found := false
		for _, i := range rows {
			if i.Branch != nil && i.Branch.Ref == b.Ref {
				found = true
				break
			}
		}
		if found {
			continue
		}
		i := base
		i.Scope = "branch"
		i.ID = digest([]string{g.GitCommonDir, b.Ref})
		i.Branch = &b
		i.Actions = []Action{}
		i.Findings = append([]Finding{}, base.Findings...)
		for _, t := range joined.OtherTasks {
			if t.Branch == strings.TrimPrefix(b.Ref, "refs/heads/") {
				i.Tasks = append(i.Tasks, t)
			}
		}
		s.branchFindings(&i)
		s.lifecycleCandidates(&i, runtimeOK && metadataOK && pe == nil)
		s.finishItem(&i, pref, now, opts.StaleDays)
		rows = append(rows, i)
	}
	for _, i := range rows {
		if i.LastActivity.After(base.LastActivity) {
			base.LastActivity = i.LastActivity
		}
	}
	if a, ok := assets[base.Path]; ok {
		base.Kind = a.kind
		base.CatalogID = a.id
		base.Phase = a.phase
		base.History = a.history
	}
	if len(rows) == 0 || len(base.Actions) > 0 || len(base.Findings) > 0 {
		base.Fingerprint = digest([]any{top, base.Findings})
		s.finishItem(&base, pref, now, opts.StaleDays)
		rows = append(rows, base)
	}
	return rows
}

func (s *Service) branchFindings(i *Item) {
	b := i.Branch
	if b.CommittedAt.After(i.LastActivity) {
		i.LastActivity = b.CommittedAt
	}
	switch {
	case b.Upstream == "" || b.Remote == ".":
		i.add("no-upstream", "No remote upstream configured; decide whether to publish or keep local", 2, true)
	case !b.ComparisonKnown:
		i.add("upstream-unavailable", "Upstream ref is unavailable locally; fetch before deciding", 2, true)
	case b.Ahead > 0 && b.Behind > 0:
		i.add("diverged", fmt.Sprintf("ahead %d, behind %d; review rebase/merge individually", b.Ahead, b.Behind), 2, true)
	case b.Ahead > 0:
		i.add("ahead", fmt.Sprintf("%d commits ahead of cached upstream", b.Ahead), 2, true)
		i.Actions = append(i.Actions, Action{Name: "push", Remote: b.Remote, Availability: "candidate"})
	case b.Behind > 0:
		i.add("behind", fmt.Sprintf("%d commits behind cached upstream", b.Behind), 3, false)
		if i.Scope == "checkout" {
			i.Actions = append(i.Actions, Action{Name: "fast-forward", Remote: b.Remote, Availability: "candidate"})
		}
	}
}

func (s *Service) lifecycleCandidates(i *Item, known bool) {
	if i.Kind == "try" || i.History {
		return
	}
	if len(i.Tasks) == 1 {
		t := i.Tasks[0]
		switch t.State {
		case task.Hot, task.Warm:
			i.Actions = append(i.Actions, Action{Name: "park-warm", Availability: "candidate"})
			if t.EffectiveMode() != task.ModeDirect {
				i.Actions = append(i.Actions, Action{Name: "park-cold", Availability: "candidate"})
			}
		case task.Done:
			i.Actions = append(i.Actions, Action{Name: "retire", Availability: "candidate"})
		}
	}
	if len(i.Tasks) == 0 && i.Worktree != nil && !i.Worktree.Main {
		i.Actions = append(i.Actions, Action{Name: "remove-checkout", Availability: "candidate"})
	}
	for n := range i.Actions {
		a := &i.Actions[n]
		if a.Name == "push" {
			continue
		}
		blocked := ""
		if !known || !i.Complete {
			blocked = "observation incomplete"
		}
		for _, f := range i.Findings {
			if f.Code == "agent-occupied" || f.Code == "harness-owned" || f.Code == "checkout-protected" || f.Code == "git-operation" || f.Code == "nested-repository" {
				blocked = f.Detail
			}
		}
		if a.Name != "park-warm" && i.Status != nil && i.Status.Dirty() {
			blocked = "uncommitted work requires individual review"
		}
		if a.Name == "remove-checkout" || a.Name == "retire" || a.Name == "park-cold" {
			for _, p := range i.Ignored {
				if !p.Disposable {
					blocked = "ignored files outside declared disposable directories"
				}
			}
		}
		if blocked != "" {
			a.Availability = "blocked"
			a.Reason = blocked
		}
	}
}

func (s *Service) finishItem(i *Item, p Preferences, now time.Time, days int) {
	if !i.LastActivity.IsZero() && now.Sub(i.LastActivity) >= time.Duration(days)*24*time.Hour {
		i.add("idle", "Idle candidate; age does not authorize removal or prove a backup", 5, false)
	}
	i.Fingerprint = digest([]any{i.Branch, i.ContentsFingerprint, i.LastActivity, i.Findings, i.Tasks})
	if intent, ok := p.Intents[i.ID]; ok && intent.Fingerprint == i.Fingerprint && (intent.Kind == "local" || now.Before(intent.Until)) && i.Complete && i.Rank() > 0 {
		i.Deferred = intent.Kind
	}
	if i.History {
		for n := range i.Actions {
			i.Actions[n].Availability = "blocked"
			i.Actions[n].Reason = "historical item: use its individual lifecycle"
		}
	}
}
func (s *Service) decorateIntent(i *Item, now time.Time) {
	p, e := s.Store.Read(i.RepositoryID)
	if e == nil {
		s.finishItem(i, p, now, 14)
	}
}
