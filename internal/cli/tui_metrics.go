package cli

import (
	"context"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
	"github.com/daviddwlee84/dev-cli/internal/snippet"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func newTUIRemoteMetrics(current func() *App) func(context.Context, []tui.RemoteRow, func([]tui.RemoteRow)) error {
	return func(ctx context.Context, rows []tui.RemoteRow, emit func([]tui.RemoteRow)) error {
		sourceID := remoteCacheSourceID(current())
		repos := make([]forge.RemoteRepo, 0, len(rows))
		for _, row := range rows {
			key := strings.Split(forge.RepoMetricsKey(row.Repo), "\x00")
			if (row.Repo.Forge == forge.GitHub || row.Repo.Forge == forge.GitLab) &&
				(len(key) != 3 || !strings.EqualFold(key[1], forge.ConfiguredHost(row.Repo.Forge))) {
				failure := forgemetrics.Failed(time.Now(), "repository does not match the configured metrics endpoint")
				row.Repo.Metrics = forgemetrics.Merge(row.Repo.Metrics, &forgemetrics.Metrics{Stars: failure, Forks: failure, OpenIssues: failure, OpenPRs: failure})
				if ctx.Err() == nil {
					emit([]tui.RemoteRow{row})
				}
				continue
			}
			repos = append(repos, row.Repo)
		}
		return forge.RefreshRepoMetrics(ctx, repos, func(patches []forge.RemoteRepo) {
			if ctx.Err() != nil {
				return
			}
			// Merge into the current cache under its writer lock, never publish
			// this producer's old inventory snapshot after another refresh.
			_ = forge.MergeCachedRepoMetrics(ctx, remoteCachePath(), sourceID, patches)
			out := make([]tui.RemoteRow, 0, len(patches))
			for _, patch := range patches {
				out = append(out, tui.RemoteRow{Repo: patch})
			}
			emit(out)
		})
	}
}

func retainRemoteMetrics(previous, next []forge.RemoteRepo) []forge.RemoteRepo {
	old := map[string]*forgemetrics.Metrics{}
	for _, row := range previous {
		if key := forge.RepoMetricsKey(row); key != "" {
			old[key] = row.Metrics
		}
	}
	out := append([]forge.RemoteRepo(nil), next...)
	for i := range out {
		out[i].Metrics = forgemetrics.Merge(old[forge.RepoMetricsKey(out[i])], out[i].Metrics)
	}
	return out
}

func snippetMetricItem(row tui.SnippetRow) snippet.Item {
	return snippet.Item{
		Identity: snippet.Identity{Forge: snippet.Kind(row.Provider), Host: row.Host, ID: row.ID, Project: row.Project, ProjectID: row.ProjectID},
		NodeID:   row.NodeID, Owner: row.Owner, URL: row.URL, Visibility: row.Visibility, Metrics: forgemetrics.Clone(row.Metrics),
	}
}
