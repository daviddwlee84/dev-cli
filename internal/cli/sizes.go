package cli

import (
	"context"

	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func sizeColumn(usage *diskusage.Usage, err error) string {
	if err != nil {
		return "?"
	}
	if usage == nil {
		return "—"
	}
	value := usage.HumanOwned()
	if usage.SharedGitBytes != nil {
		value += "+S"
	}
	return value
}

type sizeMeasurement struct {
	Usage *diskusage.Usage
	Err   error
}

func measureTargets(ctx context.Context, app *App, targets []diskusage.Target, force bool) map[string]sizeMeasurement {
	measured := make(map[string]sizeMeasurement, len(targets))
	if app == nil || app.Sizes == nil {
		return measured
	}
	load := app.Sizes.Start(ctx, targets, force)
	for result := range load.Results {
		measurement := sizeMeasurement{Err: result.Err}
		if result.Err == nil {
			usage := result.Usage
			measurement.Usage = &usage
		}
		measured[result.Key] = measurement
	}
	return measured
}

func measureRepoRows(ctx context.Context, app *App, rows []tui.RepoRow, force bool) {
	targets := make([]diskusage.Target, 0, len(rows))
	for _, row := range rows {
		if row.SizeTarget.Checkout != "" {
			targets = append(targets, row.SizeTarget)
		}
	}
	measured := measureTargets(ctx, app, targets, force)
	for index := range rows {
		measurement, ok := measured[rows[index].SizeTarget.Key]
		if !ok {
			continue
		}
		rows[index].Usage, rows[index].SizeError = measurement.Usage, measurement.Err
	}
}

func sizeTargetForExperiment(ctx context.Context, item experiment.Item) diskusage.Target {
	if item.Live.CurrentPath == "" {
		return diskusage.Target{}
	}
	repository := item.Live.Repo
	if repository == nil {
		if discovered, err := gitx.Discover(ctx, item.Live.CurrentPath); err == nil {
			repository = &discovered
		}
	}
	if repository == nil {
		return diskusage.Plain(item.Live.CurrentPath)
	}
	worktreeCount := 0
	if worktrees, err := gitx.Worktrees(ctx, item.Live.CurrentPath); err == nil && len(worktrees) > 1 {
		worktreeCount = len(worktrees) - 1
	}
	return diskusage.FromGit(*repository, worktreeCount)
}

func measureTryItems(ctx context.Context, app *App, items []experiment.Item, force bool) map[string]sizeMeasurement {
	targets := make([]diskusage.Target, 0, len(items))
	keys := make(map[string]string, len(items))
	for _, item := range items {
		target := sizeTargetForExperiment(ctx, item)
		if target.Checkout == "" {
			continue
		}
		targets = append(targets, target)
		keys[item.ID] = target.Key
	}
	byTarget := measureTargets(ctx, app, targets, force)
	byID := make(map[string]sizeMeasurement, len(keys))
	for id, key := range keys {
		if measurement, ok := byTarget[key]; ok {
			byID[id] = measurement
		}
	}
	return byID
}
