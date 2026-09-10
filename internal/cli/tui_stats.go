package cli

import (
	"context"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/stats"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func statsPanel(ctx context.Context, app *App, target repo.Repo, refresh, force bool) (tui.StatsPanel, error) {
	store, err := stats.Open(stats.Path(app.Cfg.StateDir()))
	if err != nil {
		return tui.StatsPanel{}, err
	}
	defer store.Close()
	if refresh {
		if err := store.RefreshGit(ctx, target, force); err != nil {
			return tui.StatsPanel{}, err
		}
	}
	now := time.Now()
	totals, err := store.DayTotals(stats.Query{Repo: target.Name, ExactRepo: true, Until: now})
	if err != nil {
		return tui.StatsPanel{}, err
	}
	years := stats.Years(totals, now)
	panel := tui.StatsPanel{Repo: target.Name, Years: years, Until: now}
	for _, year := range years {
		panel.Seconds += year.Seconds
		panel.ActiveDays += year.ActiveDays
	}
	if len(years) > 0 {
		panel.Since = time.Date(years[0].Year, 1, 1, 0, 0, 0, 0, now.Location())
	}
	return panel, nil
}
