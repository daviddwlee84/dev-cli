package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

// Preferences are read from the durable catalog, never from the observation
// cache. This snapshot is read-only and scoped to one load generation.
type tuiRepoCatalog struct {
	entries []*catalog.Entry
	seeds   []tui.RepoRow
	host    string
}

func readTUIRepoCatalog(ctx context.Context, app *App) (tuiRepoCatalog, error) {
	out := tuiRepoCatalog{host: configHostname()}
	if app.Catalog == nil {
		return out, nil
	}
	entries, diagnostics, err := app.Catalog.ListWithDiagnostics()
	if err != nil {
		return out, fmt.Errorf("catalog metadata: %w", err)
	}
	out.entries = entries
	for _, diagnostic := range diagnostics {
		err = errors.Join(err, diagnostic)
	}
	claimed := map[string]string{}
	for _, entry := range entries {
		location, ok := entry.LocationFor(out.host)
		if !ok || location.State != catalog.LocationPresent {
			continue
		}
		for _, key := range tuiRepoKeys(repo.Repo{Path: location.CurrentPath, RealPath: location.RealPath, CommonDir: location.GitCommonDir}) {
			if owner := claimed[key]; owner != "" && owner != entry.ID {
				err = errors.Join(err, fmt.Errorf("catalog assets %s and %s match the same local repository", owner, entry.ID))
			} else {
				claimed[key] = entry.ID
			}
		}
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return out, errors.Join(err, ctx.Err())
		}
		if !entry.Favorite || entry.Kind != catalog.KindRepository {
			continue
		}
		location, ok := entry.LocationFor(out.host)
		if !ok || location.CurrentPath == "" {
			continue
		}
		for _, root := range app.Cfg.DiscoveryRoots() {
			covered, scopeErr := repo.PathDiscoverableFromRoot(root, location.CurrentPath, repo.DefaultOptions())
			if scopeErr != nil {
				err = errors.Join(err, fmt.Errorf("favorite %s discovery scope: %w", entry.Title(), scopeErr))
				continue
			}
			if !covered {
				continue
			}
			category := ""
			if relative, relErr := filepath.Rel(root, location.CurrentPath); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				category = filepath.ToSlash(filepath.Dir(relative))
				if category == "." {
					category = ""
				}
			}
			out.seeds = append(out.seeds, tui.RepoRow{
				Repo: repo.Repo{Name: filepath.Base(location.CurrentPath), Path: location.CurrentPath,
					RealPath: location.RealPath, CommonDir: location.GitCommonDir,
					Root: root, Category: category, HasGit: location.GitCommonDir != ""},
				Asset: entry.Clone(), Pending: "loading",
			})
			break
		}
	}
	return out, err
}

func (c tuiRepoCatalog) asset(repository repo.Repo) *catalog.Entry {
	var match *catalog.Entry
	for _, entry := range c.entries {
		location, ok := entry.LocationFor(c.host)
		if !ok || location.State != catalog.LocationPresent || !repoLocationMatches(repository, location) {
			continue
		}
		if match != nil {
			// Ambiguous metadata must not acquire authority through discovery.
			return nil
		}
		match = entry
	}
	if match == nil {
		return nil
	}
	return match.Clone()
}

func discoverTUIFavorite(ctx context.Context, expected repo.Repo) (repo.Repo, error) {
	if _, err := repo.ValidateNavigation(ctx, expected, ""); err != nil {
		return expected, err
	}
	g, err := gitx.Discover(ctx, expected.Path)
	if err != nil {
		if !expected.HasGit && errors.Is(err, gitx.ErrNotARepo) {
			// A failed Git marker is not an explicitly observed plain directory.
			if _, markerErr := os.Lstat(filepath.Join(expected.Path, ".git")); os.IsNotExist(markerErr) {
				expected.RealPath = canonicalRepoValue(expected.Path)
				return expected, nil
			}
		}
		// Discover's --show-toplevel is rejected by bare hubs. Navigation has
		// already checked the expected identity; obtain the actual bare fields
		// with Git rather than accepting a filesystem skeleton as authority.
		out, bareErr := gitx.RunReadOnly(ctx, expected.Path, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--is-bare-repository")
		fields := strings.Split(strings.TrimSpace(out), "\n")
		if bareErr != nil || len(fields) != 3 || fields[2] != "true" {
			return expected, err
		}
		g.GitDir, g.GitCommonDir = canonicalRepoValue(fields[0]), canonicalRepoValue(fields[1])
		if g.GitDir == "" || g.GitCommonDir == "" {
			return expected, err
		}
		g.MainRoot, g.Bare = g.GitCommonDir, true
	}
	if !expected.HasGit && !g.Bare && canonicalRepoValue(expected.Path) != g.Root {
		// Preserve an observed plain directory below another repository; Git's
		// ancestor lookup must not silently change which target this row means.
		expected.RealPath = canonicalRepoValue(expected.Path)
		return expected, nil
	}
	if g.IsLinkedWorktree {
		return expected, errors.New("favorite target is a linked worktree, not a repository root")
	}
	expected.RealPath, expected.GitDir, expected.CommonDir = g.MainRoot, g.GitDir, g.GitCommonDir
	expected.MainRoot, expected.HasGit, expected.Bare = g.MainRoot, true, g.Bare
	if info, statErr := os.Lstat(expected.Path); statErr == nil {
		expected.Symlink = info.Mode()&os.ModeSymlink != 0
	}
	return expected, nil
}

// A targeted physical-path seed must not erase the human navigation alias
// chosen by discovery. Presentation changes do not repeat Git enrichment.
func preferTUIRepoAlias(row *tui.RepoRow, alias repo.Repo) bool {
	if !alias.Symlink || row.Repo.Symlink {
		return false
	}
	row.Repo.Path, row.Repo.Name = alias.Path, alias.Name
	row.Repo.Root, row.Repo.Category, row.Repo.Symlink = alias.Root, alias.Category, true
	if row.Context.Repo.Path != "" {
		row.Context.Repo = row.Repo
	}
	return true
}

// Keys use physical paths and Git common directories, never display names.
// Seed keys reserve only potentially matching work until target validation;
// only freshly validated keys authorize deduplication of enrichment.
func tuiRepoKeys(r repo.Repo) []string {
	var keys []string
	if common := canonicalRepoValue(r.CommonDir); common != "" {
		keys = append(keys, "git:"+common)
	}
	for path := range canonicalRepoValues(r.Path, r.RealPath) {
		keys = append(keys, "path:"+path)
	}
	return keys
}
