package stats_test

import (
	"context"
	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/stats"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryBackfillCheckpointAndOldCommits(t *testing.T) {
	git := gittest.New(t)
	store := open(t)
	ctx := context.Background()
	target := repo.Repo{Name: "demo", Path: git.Root}
	git.Git("commit", "--allow-empty", "--date=2020-02-29T12:00:00Z", "-m", "old work")
	if err := store.RefreshGit(ctx, target, false); err != nil {
		t.Fatal(err)
	}
	totals, err := store.DayTotals(stats.Query{Repo: "demo", ExactRepo: true, Until: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 2 {
		t.Fatal(totals)
	}
	date := time.Unix(1582977600, 0).In(time.Local)
	if err := store.Set(stats.Entry{Day: date, Repo: "demo", Source: stats.SourceGit, Seconds: 7}); err != nil {
		t.Fatal(err)
	}
	if err := store.RefreshGit(ctx, target, false); err != nil {
		t.Fatal(err)
	}
	totals, _ = store.DayTotals(stats.Query{Repo: "demo", ExactRepo: true, Until: time.Now()})
	if totals[date.Format("2006-01-02")] != 7 {
		t.Fatal("unchanged refs did not use checkpoint")
	}
	if err := store.RefreshGit(ctx, target, true); err != nil {
		t.Fatal(err)
	}
	totals, _ = store.DayTotals(stats.Query{Repo: "demo", ExactRepo: true, Until: time.Now()})
	if totals[date.Format("2006-01-02")] != 1200 {
		t.Fatal(totals)
	}
	git.Git("commit", "--allow-empty", "--date=2022-04-05T12:00:00Z", "-m", "backdated work")
	if err := store.RefreshGit(ctx, target, false); err != nil {
		t.Fatal(err)
	}
	totals, _ = store.DayTotals(stats.Query{Repo: "demo", ExactRepo: true, Until: time.Now()})
	if len(totals) != 3 {
		t.Fatal(totals)
	}
	if _, err := store.Clear(stats.ClearQuery{Repo: "demo", Sources: []stats.Source{stats.SourceGit}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RefreshGit(ctx, target, false); err != nil {
		t.Fatal(err)
	}
	totals, _ = store.DayTotals(stats.Query{Repo: "demo", ExactRepo: true, Until: time.Now()})
	if len(totals) != 3 {
		t.Fatal("clear left a stale checkpoint")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.RefreshGit(canceled, target, true); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestCalendarYearsKeepGapsAndWrapWithoutDroppingDates(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	years := stats.Years(map[string]int{"2020-02-29": 1200, "2026-01-01": 2400, "2027-01-01": 900}, now)
	if len(years) != 7 || years[1].ActiveDays != 0 || years[0].Year != 2020 {
		t.Fatal(years)
	}
	for _, width := range []int{28, 65, 120} {
		output := stats.YearHeatmaps(years, width)
		if !strings.Contains(output, "2020") || !strings.Contains(output, "2026") {
			t.Fatal(output)
		}
		shaded := 0
		for _, r := range output {
			if r == '░' || r == '▒' || r == '▓' || r == '█' {
				shaded++
			}
		}
		if shaded != 2 {
			t.Fatalf("width %d omitted/duplicated activity: %d", width, shaded)
		}
		for _, line := range strings.Split(output, "\n") {
			if !strings.Contains(line, "active days") && lipgloss.Width(line) > width {
				t.Fatalf("width %d: %q", width, line)
			}
		}
	}
}

func TestHistoryBackfillsBareRepositories(t *testing.T) {
	git := gittest.New(t)
	bare := filepath.Join(t.TempDir(), "archive.git")
	git.Git("clone", "--bare", git.Root, bare)
	store := open(t)
	if err := store.RefreshGit(context.Background(), repo.Repo{Name: "archive", Path: bare, Bare: true}, false); err != nil {
		t.Fatal(err)
	}
	totals, err := store.DayTotals(stats.Query{Repo: "archive", ExactRepo: true, Until: time.Now()})
	if err != nil || len(totals) == 0 {
		t.Fatal(totals, err)
	}
}
