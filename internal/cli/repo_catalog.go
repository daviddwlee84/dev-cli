package cli

import (
	"sort"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

// joinRepoAssets matches a discovery snapshot without persisting every repo.
// complete is false when unreadable catalog records could make a negative or
// duplicate match unsafe to act on.
func joinRepoAssets(app *App, repos []repo.Repo) (assets []*catalog.Entry, complete bool, err error) {
	assets = make([]*catalog.Entry, len(repos))
	if app == nil || app.Catalog == nil {
		return assets, true, nil
	}
	entries, diagnostics, err := app.Catalog.ListWithDiagnostics()
	if err != nil {
		app.warnf("could not read catalog metadata: %v; leaving repository rows unsuppressed", err)
		return assets, false, nil
	}
	for _, diagnostic := range diagnostics {
		app.warnf("%s", diagnostic.Error())
	}
	complete = len(diagnostics) == 0
	host := configHostname()
	for index, repository := range repos {
		var matches []*catalog.Entry
		for _, entry := range entries {
			location, ok := entry.LocationFor(host)
			if !ok || location.State != catalog.LocationPresent {
				continue
			}
			if repoLocationMatches(repository, location) {
				matches = append(matches, entry)
			}
		}
		catalog.Sort(matches)
		switch len(matches) {
		case 1:
			assets[index] = matches[0].Clone()
		case 0:
		default:
			ids := make([]string, len(matches))
			for i, entry := range matches {
				ids[i] = entry.ID
			}
			sort.Strings(ids)
			app.warnf("repository %s matches multiple catalog assets (%v); leaving it unsuppressed", repository.Path, ids)
		}
	}
	return assets, complete, nil
}

func repoLocationMatches(repository repo.Repo, location catalog.Location) bool {
	repositoryPaths := canonicalRepoValues(repository.Path, repository.RealPath)
	locationPaths := canonicalRepoValues(location.CurrentPath, location.RealPath)
	for value := range repositoryPaths {
		if _, ok := locationPaths[value]; ok {
			return true
		}
	}
	common := canonicalRepoValue(repository.CommonDir)
	return common != "" && common == canonicalRepoValue(location.GitCommonDir)
}

func canonicalRepoValues(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if canonical := canonicalRepoValue(value); canonical != "" {
			out[canonical] = struct{}{}
		}
	}
	return out
}

func canonicalRepoValue(value string) string {
	if value == "" {
		return ""
	}
	canonical, err := pathx.Canonical(value)
	if err != nil {
		return ""
	}
	return canonical
}

func suppressTryRepo(entry *catalog.Entry, catalogComplete bool) bool {
	if entry == nil || !catalogComplete || entry.Kind != catalog.KindTry || entry.Experiment == nil {
		return false
	}
	return entry.Experiment.Phase == catalog.PhaseActive || entry.Experiment.Phase == catalog.PhaseDeprecated
}

func configHostname() string {
	// Kept behind a function to make every matching call visibly host-scoped.
	return config.Hostname()
}
