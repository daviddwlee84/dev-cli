package cli

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
)

func TestTUIReleaseObservationUsesBuildComparisonWithoutDailyNudge(t *testing.T) {
	oldVersion := Version
	t.Cleanup(func() { Version = oldVersion })
	for _, tc := range []struct {
		version string
		newer   bool
	}{
		{"v0.2.42", true}, {"v0.2.42-4-gabc1234-dirty", true}, {"v0.2.42-dirty", true},
		{"v0.2.43", false}, {"v0.2.44", false}, {"dev", false},
		{"v0.2.43-0.20260922042323-da7d7ef4d6ca", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			Version = tc.version
			cached := releaseCheck{TagName: "v0.2.43", CheckedAt: time.Now(), NudgedAt: time.Now()}
			got := tuiReleaseObservation(cached, true)
			if got.Newer != tc.newer || got.Stale || got.Latest != cached.TagName || got.CheckedAt != cached.CheckedAt {
				t.Fatalf("observation = %+v, newer=%t", got, tc.newer)
			}
		})
	}
}

func TestTUIReleaseActionsRefreshExpiredCacheWithoutConsumingNudge(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DEV_NO_UPDATE_CHECK", "")
	oldVersion := Version
	Version = "v0.2.42"
	t.Cleanup(func() { Version = oldVersion })
	nudged := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeReleaseCheck(releaseCheck{TagName: "v0.2.43", CheckedAt: time.Now().Add(-25 * time.Hour), NudgedAt: nudged})
	calls := 0
	withUpgradeTransport(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != releasesURL {
			t.Fatalf("unexpected URL: %s", req.URL)
		}
		return upgradeResponse(200, `{"tag_name":"v0.2.44"}`), nil
	})
	current := &App{Cfg: config.Default()}
	actions := newTUIReleaseActions(func() *App { return current })
	if calls != 0 || !actions.Cached.Stale || !actions.Cached.Newer {
		t.Fatalf("startup cache used network or lost cached hint: calls=%d cached=%+v", calls, actions.Cached)
	}
	got := actions.Check(t.Context())
	if calls != 1 || got.Err != nil || got.Stale || !got.Newer || got.Latest != "v0.2.44" {
		t.Fatalf("check = %+v, calls=%d", got, calls)
	}
	cached, fresh := readReleaseCheck()
	if !fresh || !cached.NudgedAt.Equal(nudged) {
		t.Fatalf("check consumed CLI daily nudge: %+v", cached)
	}
	_ = actions.Check(t.Context())
	if calls != 1 {
		t.Fatal("fresh cache triggered another network request")
	}
}

func TestTUIReleaseActionsUseCurrentToggleAndEnvironment(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DEV_NO_UPDATE_CHECK", "")
	withUpgradeTransport(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("disabled update check contacted network")
		return nil, errors.New("unexpected network")
	})
	state := newTUIAppState(&App{Cfg: config.Default()})
	actions := newTUIReleaseActions(state.Current)
	state.Commit(&App{Cfg: configWithUpdateCheck(false)})
	if got := actions.Check(t.Context()); got.Latest != "" || got.Err != nil {
		t.Fatalf("disabled callback = %+v", got)
	}
	state.Commit(&App{Cfg: config.Default()})
	t.Setenv("DEV_NO_UPDATE_CHECK", "1")
	if got := actions.Check(t.Context()); got.Latest != "" || got.Err != nil {
		t.Fatalf("environment-disabled callback = %+v", got)
	}
}

func TestTUIReleaseActionsOfflineAndCanceledRetainCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DEV_NO_UPDATE_CHECK", "")
	writeReleaseCheck(releaseCheck{TagName: "v0.2.43", CheckedAt: time.Now().Add(-25 * time.Hour)})
	calls := 0
	withUpgradeTransport(t, func(req *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("offline")
	})
	app := &App{Cfg: config.Default()}
	actions := newTUIReleaseActions(func() *App { return app })
	got := actions.Check(t.Context())
	if got.Err == nil || got.Latest != "v0.2.43" || !got.Stale || calls != 1 {
		t.Fatalf("offline observation = %+v, calls=%d", got, calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got = actions.Check(ctx)
	if !errors.Is(got.Err, context.Canceled) || calls != 1 || got.Latest != "v0.2.43" {
		t.Fatalf("canceled observation = %+v, calls=%d", got, calls)
	}
}

func TestDashboardInvocationSeparatesReleaseChecksFromCLIAndFlow(t *testing.T) {
	app := &App{interactiveCheck: func() bool { return true }}
	root := newRootCommand(app)
	for _, tc := range []struct {
		args      []string
		dashboard bool
	}{
		{nil, true}, {[]string{"tui"}, true}, {[]string{"tui", "tools"}, false},
		{[]string{"flow"}, false}, {[]string{"ls"}, false}, {[]string{"list"}, false},
	} {
		command, _, err := root.Find(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		if got := dashboardInvocation(command, app); got != tc.dashboard {
			t.Fatalf("dashboardInvocation(%v) = %t", tc.args, got)
		}
	}
	app.interactiveCheck = func() bool { return false }
	if dashboardInvocation(root, app) {
		t.Fatal("piped bare dev must retain ordinary CLI behavior")
	}
}
