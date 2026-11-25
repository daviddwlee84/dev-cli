package triage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/daviddwlee84/dev-cli/internal/wt"
	"github.com/dustin/go-humanize"
)

// ActionPruneDeps deletes regenerable dependency/build directories in place.
// The checkout, its branch and every tracked or unignored file are retained.
const ActionPruneDeps = "prune-deps"

// DependencyDir is a wholly ignored, regenerable directory inside a checkout.
// Bytes is the apparent size of its ignored files; APFS clones and hard links
// can make the space actually returned smaller.
type DependencyDir struct {
	Path     string    `json:"path"`
	Bytes    int64     `json:"bytes"`
	Files    int       `json:"files"`
	Rebuild  string    `json:"rebuild,omitempty"`
	Newest   time.Time `json:"newest"`
	identity string
}

// dependencyRule recognizes one kind of regenerable directory. Generic names
// such as target or deps also require their ecosystem marker so an ignored
// directory that merely shares the name is never proposed.
type dependencyRule struct {
	name     string
	parent   string   // required parent directory name (vendor/bundle)
	sibling  []string // any one must exist next to the directory (or next to parent)
	inside   string   // must exist inside the directory
	fallback string
}

var dependencyRules = []dependencyRule{
	{name: ".venv", inside: "pyvenv.cfg", fallback: "recreate the virtualenv (for example uv sync)"},
	{name: "venv", inside: "pyvenv.cfg", fallback: "recreate the virtualenv (for example uv sync)"},
	{name: "node_modules", sibling: []string{"package.json"}, fallback: "reinstall packages (npm/pnpm/yarn/bun install)"},
	{name: "target", sibling: []string{"Cargo.toml"}, fallback: "cargo build"},
	{name: "bundle", parent: "vendor", sibling: []string{"Gemfile"}, fallback: "bundle install"},
	{name: "deps", sibling: []string{"mix.exs"}, fallback: "mix deps.get"},
	{name: "_build", sibling: []string{"mix.exs"}, fallback: "mix compile"},
	{name: ".next", sibling: []string{"package.json"}, fallback: "rebuilt by the next build"},
	{name: ".turbo", sibling: []string{"package.json", "turbo.json"}, fallback: "rebuilt by the next build"},
	{name: ".pytest_cache", fallback: "regenerated automatically"},
	{name: ".ruff_cache", fallback: "regenerated automatically"},
	{name: ".mypy_cache", fallback: "regenerated automatically"},
}

func matchRule(parts []string, k int) (dependencyRule, bool) {
	name := parts[k-1]
	for _, r := range dependencyRules {
		if r.name != name {
			continue
		}
		if r.parent != "" && (k < 2 || parts[k-2] != r.parent) {
			continue
		}
		return r, true
	}
	return dependencyRule{}, false
}

func (r dependencyRule) recognized(checkout, rel string) bool {
	full := filepath.Join(checkout, filepath.FromSlash(rel))
	base := filepath.Dir(full)
	if r.parent != "" {
		base = filepath.Dir(base)
	}
	if r.inside != "" {
		if info, err := os.Lstat(filepath.Join(full, r.inside)); err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	if len(r.sibling) == 0 {
		return true
	}
	for _, s := range r.sibling {
		if _, err := os.Lstat(filepath.Join(base, s)); err == nil {
			return true
		}
	}
	return false
}

// rebuildHint prefers the lockfile-derived install command used by worktree
// provisioning, so the preview names the same command dev itself would run.
func (r dependencyRule) rebuildHint(checkout, rel string) string {
	base := filepath.Dir(filepath.Join(checkout, filepath.FromSlash(rel)))
	want := r.name
	if r.parent != "" {
		base = filepath.Dir(base)
		want = r.parent + "/" + r.name
	}
	for _, e := range wt.DetectEcosystems(base) {
		for _, d := range e.DepDirs {
			if d == want && e.Install != "" {
				return e.Install
			}
		}
	}
	if (r.name == "venv" || r.name == ".venv") && fileExists(filepath.Join(base, "pyproject.toml")) {
		return "uv sync"
	}
	return r.fallback
}

func fileExists(p string) bool { _, err := os.Lstat(p); return err == nil }

// DependencyDirs derives regenerable directories from an already inspected
// ignored-file list. Only the outermost recognized directory is reported, and
// a directory qualifies only when it is a real directory, contains no tracked
// or unignored untracked file, and holds no nested repository.
func DependencyDirs(ctx context.Context, checkout string, contents gitx.CheckoutContents) ([]DependencyDir, error) {
	type acc struct {
		rule dependencyRule
		dir  DependencyDir
	}
	found := map[string]*acc{}
	rejected := map[string]bool{}
	for _, p := range contents.Ignored {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		parts := strings.Split(strings.TrimSuffix(p.Path, "/"), "/")
		for k := 1; k < len(parts); k++ {
			rule, ok := matchRule(parts, k)
			if !ok {
				continue
			}
			rel := strings.Join(parts[:k], "/")
			if rejected[rel] {
				break
			}
			a := found[rel]
			if a == nil {
				if !rule.recognized(checkout, rel) {
					rejected[rel] = true
					continue
				}
				a = &acc{rule: rule, dir: DependencyDir{Path: rel}}
				found[rel] = a
			}
			a.dir.Bytes += p.Bytes
			a.dir.Files++
			if p.Modified.After(a.dir.Newest) {
				a.dir.Newest = p.Modified
			}
			break
		}
	}
	out := []DependencyDir{}
	for rel, a := range found {
		if nestedWithin(contents.Nested, rel) {
			continue
		}
		identity, err := Identity(filepath.Join(checkout, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		if err := wholeIgnored(ctx, checkout, rel); err != nil {
			continue
		}
		a.dir.identity = identity
		a.dir.Rebuild = a.rule.rebuildHint(checkout, rel)
		out = append(out, a.dir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func nestedWithin(nested []string, rel string) bool {
	for _, n := range nested {
		if n == rel || strings.HasPrefix(n, rel+"/") || strings.HasPrefix(rel, n+"/") {
			return true
		}
	}
	return false
}

// wholeIgnored proves that deleting rel cannot discard tracked content or an
// untracked file that Git would otherwise show as work.
func wholeIgnored(ctx context.Context, checkout, rel string) error {
	tracked, err := gitx.Run(ctx, checkout, "ls-files", "-z", "--", ":(literal)"+rel+"/")
	if err != nil {
		return err
	}
	if tracked != "" {
		return errors.New("directory contains tracked files")
	}
	untracked, err := gitx.Run(ctx, checkout, "ls-files", "--others", "--exclude-standard", "-z", "--", ":(literal)"+rel+"/")
	if err != nil {
		return err
	}
	if untracked != "" {
		return errors.New("directory contains unignored untracked files")
	}
	return nil
}

// pruneActivity is the checkout's newest activity excluding dependency
// directories, so a recent install does not make an idle checkout look active.
func pruneActivity(i Item, dirs []DependencyDir) time.Time {
	var newest time.Time
	bump := func(t time.Time) {
		if t.After(newest) {
			newest = t
		}
	}
	if i.Status != nil {
		bump(i.Status.LatestChange)
	}
	for _, p := range i.Ignored {
		inside := false
		for _, d := range dirs {
			if strings.HasPrefix(p.Path, d.Path+"/") {
				inside = true
				break
			}
		}
		if !inside {
			bump(p.Modified)
		}
	}
	if i.Branch != nil {
		bump(i.Branch.CommittedAt)
	}
	for _, t := range i.Tasks {
		bump(t.Updated)
	}
	return newest
}

func dependencyTotal(dirs []DependencyDir) int64 {
	var n int64
	for _, d := range dirs {
		n += d.Bytes
	}
	return n
}

// pruneCandidate adds the regenerable finding and prune-deps action. It must
// run after branch/task/runtime facts are attached to the checkout row.
func (s *Service) pruneCandidate(i *Item, known bool, now time.Time, days int) {
	if i.Scope != "checkout" || len(i.Regenerable) == 0 {
		return
	}
	total := dependencyTotal(i.Regenerable)
	i.add("regenerable", fmt.Sprintf("%d regenerable directories, about %s (dependencies and build output)", len(i.Regenerable), humanize.IBytes(uint64(total))), 4, false)
	a := Action{Name: ActionPruneDeps, Availability: "candidate"}
	activity := pruneActivity(*i, i.Regenerable)
	switch {
	case !known || !i.Complete:
		a.Reason = "observation incomplete"
	case i.Worktree != nil && (i.Worktree.Locked || i.Worktree.Prunable):
		a.Reason = "locked or prunable checkout: inspect individually"
	case len(i.Runtimes) > 0:
		a.Reason = "live runtime session covers checkout; close it before pruning dependencies"
	case activity.IsZero() || now.Sub(activity) < time.Duration(days)*24*time.Hour:
		a.Reason = fmt.Sprintf("checkout was active within %d days (dependency directories excluded)", days)
	}
	for _, f := range i.Findings {
		if f.Code == "agent-occupied" || f.Code == "harness-owned" || f.Code == "git-operation" {
			a.Reason = f.Detail
		}
	}
	if a.Reason != "" {
		a.Availability = "blocked"
	}
	i.Actions = append(i.Actions, a)
}

// pruneOccupancy requires fresh runtime coverage proving no session or agent
// is using the checkout, since a live process may hold the environment open.
func (s *Service) pruneOccupancy(ctx context.Context, path string) error {
	if len(s.cfg.Runtimes) == 0 {
		return errors.New("runtime coverage unavailable")
	}
	for _, rt := range s.cfg.Runtimes {
		occ, e := runtime.InspectOccupancy(ctx, rt, path, runtime.OccupancyOptions{InspectProcesses: true})
		if e != nil || !occ.SessionList.Observed() || occ.SessionCoverageErr != nil || occ.AgentActivityList.Err != nil {
			return errors.New("runtime coverage changed or is unavailable")
		}
		if len(occ.Agents) > 0 {
			return errors.New("recognized agent occupies checkout")
		}
		if len(occ.Sessions) > 0 {
			return errors.New("live runtime session covers checkout")
		}
	}
	return nil
}

func (s *Service) preparePrune(ctx context.Context, item Item, batchID string) batchEntry {
	e := batchEntry{item: item, preview: Preview{ItemID: item.ID, Path: item.Path, Action: ActionPruneDeps, Reasons: []string{}, Effects: []string{}}}
	fail := func(err error) batchEntry {
		e.preview.Reasons = append(e.preview.Reasons, SafeText(err.Error()))
		return e
	}
	if !item.Complete || item.Scope != "checkout" {
		return fail(errors.New("incomplete inventory cannot authorize pruning"))
	}
	repoIdentity, err := Identity(item.RepositoryID)
	if err != nil {
		return fail(err)
	}
	pathIdentity, err := Identity(item.Path)
	if err != nil {
		return fail(err)
	}
	prefs, err := s.Store.Read(item.RepositoryID)
	if err != nil {
		return fail(err)
	}
	contents, err := gitx.InspectTriageContents(ctx, item.Path, prefs.DisposableDirs)
	if err != nil {
		return fail(err)
	}
	dirs, err := DependencyDirs(ctx, item.Path, contents)
	if err != nil {
		return fail(err)
	}
	if len(dirs) == 0 {
		return fail(errors.New("no regenerable directories remain; refresh"))
	}
	if err := s.pruneOccupancy(ctx, item.Path); err != nil {
		return fail(err)
	}
	if contents.Status.Dirty() {
		e.preview.Effects = append(e.preview.Effects, "Uncommitted work is kept untouched · "+contents.Status.Breakdown())
	}
	for _, d := range dirs {
		e.preview.Effects = append(e.preview.Effects, fmt.Sprintf("Delete regenerable directory · %s (~%s; rebuild: %s)", d.Path, humanize.IBytes(uint64(d.Bytes)), d.Rebuild))
	}
	e.prune = dirs
	e.preview.Ready = true
	checkout := item.Path
	e.pruneApply = func(ctx context.Context) (int64, []string, error) {
		if id, err := Identity(item.RepositoryID); err != nil || id != repoIdentity {
			return 0, nil, fmt.Errorf("%w: repository directory identity changed", taskflow.ErrStalePlan)
		}
		if id, err := Identity(checkout); err != nil || id != pathIdentity {
			return 0, nil, fmt.Errorf("%w: checkout directory identity changed", taskflow.ErrStalePlan)
		}
		if err := s.pruneOccupancy(ctx, checkout); err != nil {
			return 0, nil, fmt.Errorf("%w: %s", taskflow.ErrStalePlan, err.Error())
		}
		var freed int64
		removed := []string{}
		for n, d := range dirs {
			if err := removeDependencyDir(ctx, checkout, d, fmt.Sprintf("%s-%d", shortID(batchID), n)); err != nil {
				if len(removed) == 0 {
					return freed, removed, err
				}
				return freed, removed, fmt.Errorf("%s: %w", d.Path, err)
			}
			freed += d.Bytes
			removed = append(removed, d.Path)
		}
		return freed, removed, nil
	}
	return e
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// removeDependencyDir revalidates one directory, moves it aside on the same
// filesystem so an interrupted removal never leaves a half-populated
// environment at the original path, and then deletes it.
func removeDependencyDir(ctx context.Context, checkout string, d DependencyDir, tag string) error {
	full := filepath.Join(checkout, filepath.FromSlash(d.Path))
	if id, err := Identity(full); err != nil || id != d.identity {
		return fmt.Errorf("%w: %s changed identity", taskflow.ErrStalePlan, d.Path)
	}
	if err := wholeIgnored(ctx, checkout, d.Path); err != nil {
		return fmt.Errorf("%w: %s %s", taskflow.ErrStalePlan, d.Path, err.Error())
	}
	nested := errors.New("nested repository")
	err := filepath.WalkDir(full, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == ".git" {
			return nested
		}
		return nil
	})
	if errors.Is(err, nested) {
		return fmt.Errorf("%w: %s now contains a nested repository", taskflow.ErrStalePlan, d.Path)
	}
	if err != nil {
		return err
	}
	aside := filepath.Join(filepath.Dir(full), ".dev-prune-"+filepath.Base(full)+"-"+tag)
	if _, err := os.Lstat(aside); err == nil {
		return fmt.Errorf("temporary path already exists: %s", aside)
	}
	if err := os.Rename(full, aside); err != nil {
		return err
	}
	if err := os.RemoveAll(aside); err != nil {
		return fmt.Errorf("moved aside but removal failed; delete %s manually: %w", aside, err)
	}
	return nil
}
