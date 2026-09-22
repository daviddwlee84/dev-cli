package cli

import (
	"context"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func tuiReleaseObservation(cached releaseCheck, fresh bool) tui.ReleaseObservation {
	release, _, _ := buildDescription(versionFromBuild())
	return tui.ReleaseObservation{
		Latest: cached.TagName, CheckedAt: cached.CheckedAt,
		Newer: semverLess(release, cached.TagName), Stale: !fresh,
	}
}

func newTUIReleaseActions(current func() *App) tui.ReleaseActions {
	actions := tui.ReleaseActions{Cached: tui.ReleaseObservation{Stale: true}}
	if updateCheckEnabled(current().Cfg) {
		cached, fresh := readReleaseCheck()
		actions.Cached = tuiReleaseObservation(cached, fresh)
	}
	actions.Check = func(parent context.Context) tui.ReleaseObservation {
		if !updateCheckEnabled(current().Cfg) {
			return tui.ReleaseObservation{}
		}
		cached, fresh := readReleaseCheck()
		observation := tuiReleaseObservation(cached, fresh)
		if fresh || parent.Err() != nil {
			observation.Err = parent.Err()
			return observation
		}
		ctx, cancel := context.WithTimeout(parent, 4*time.Second)
		defer cancel()
		latest, err := latestRelease(ctx, false)
		if err != nil {
			observation.Err = err
			return observation
		}
		return tuiReleaseObservation(releaseCheck{TagName: latest, CheckedAt: time.Now()}, true)
	}
	return actions
}
