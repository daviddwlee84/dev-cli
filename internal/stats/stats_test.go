package stats_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/stats"
)

func open(t *testing.T) *stats.Store {
	t.Helper()
	s, err := stats.Open(stats.Path(t.TempDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func day(s string) time.Time {
	d, _ := time.ParseInLocation("2006-01-02", s, time.Local)
	return d
}

func TestAddAccumulatesSetReplaces(t *testing.T) {
	s := open(t)
	e := stats.Entry{Day: day("2026-08-01"), Repo: "api", Source: stats.SourceSession, Seconds: 300}

	if err := s.Add(e, e); err != nil {
		t.Fatal(err)
	}
	totals, _ := s.DayTotals(stats.Query{Since: day("2026-07-01"), Until: day("2026-09-01")})
	if totals["2026-08-01"] != 600 {
		t.Errorf("Add should accumulate, got %d", totals["2026-08-01"])
	}

	// Set is for re-runnable importers: importing the same day twice must not
	// double the total.
	if err := s.Set(e); err != nil {
		t.Fatal(err)
	}
	totals, _ = s.DayTotals(stats.Query{Since: day("2026-07-01"), Until: day("2026-09-01")})
	if totals["2026-08-01"] != 300 {
		t.Errorf("Set should replace, got %d", totals["2026-08-01"])
	}
}

func TestAddIgnoresEmptyAndNonPositive(t *testing.T) {
	s := open(t)
	err := s.Add(
		stats.Entry{Day: day("2026-08-01"), Repo: "", Source: stats.SourceGit, Seconds: 100},
		stats.Entry{Day: day("2026-08-01"), Repo: "api", Source: stats.SourceGit, Seconds: 0},
		stats.Entry{Day: day("2026-08-01"), Repo: "api", Source: stats.SourceGit, Seconds: -5},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Empty() {
		t.Error("nothing valid was supplied, so nothing should have been stored")
	}
}

func TestSourcesAreSeparate(t *testing.T) {
	s := open(t)
	d := day("2026-08-01")
	s.Add(
		stats.Entry{Day: d, Repo: "api", Source: stats.SourceSession, Seconds: 600},
		stats.Entry{Day: d, Repo: "api", Source: stats.SourceWakaTime, Seconds: 900},
	)
	q := stats.Query{Since: day("2026-07-01"), Until: day("2026-09-01")}

	all, _ := s.DayTotals(q)
	if all["2026-08-01"] != 1500 {
		t.Errorf("unfiltered should sum both sources, got %d", all["2026-08-01"])
	}
	q.Sources = []stats.Source{stats.SourceSession}
	only, _ := s.DayTotals(q)
	if only["2026-08-01"] != 600 {
		t.Errorf("filtering by source should isolate it, got %d", only["2026-08-01"])
	}
}

func TestRepoTotalsRankAndCount(t *testing.T) {
	s := open(t)
	s.Add(
		stats.Entry{Day: day("2026-08-01"), Repo: "api", Source: stats.SourceGit, Seconds: 100},
		stats.Entry{Day: day("2026-08-02"), Repo: "api", Source: stats.SourceGit, Seconds: 100},
		stats.Entry{Day: day("2026-08-01"), Repo: "web", Source: stats.SourceGit, Seconds: 500},
	)
	got, err := s.RepoTotals(stats.Query{Since: day("2026-07-01"), Until: day("2026-09-01")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Repo != "web" {
		t.Fatalf("busiest repo should come first: %+v", got)
	}
	if got[1].Repo != "api" || got[1].Days != 2 {
		t.Errorf("api should show 2 distinct days: %+v", got[1])
	}
	if got[1].Last != "2026-08-02" {
		t.Errorf("Last = %q", got[1].Last)
	}
}

func TestQueryWindowExcludes(t *testing.T) {
	s := open(t)
	s.Add(
		stats.Entry{Day: day("2026-01-01"), Repo: "old", Source: stats.SourceGit, Seconds: 100},
		stats.Entry{Day: day("2026-08-01"), Repo: "new", Source: stats.SourceGit, Seconds: 100},
	)
	got, _ := s.RepoTotals(stats.Query{Since: day("2026-07-01"), Until: day("2026-09-01")})
	if len(got) != 1 || got[0].Repo != "new" {
		t.Errorf("the window should exclude the older entry: %+v", got)
	}
}

func TestHeatmapShape(t *testing.T) {
	totals := map[string]int{
		"2026-08-03": 3600,
		"2026-08-05": 60,
	}
	out := stats.Heatmap(totals, stats.HeatmapOptions{
		Since: day("2026-07-01"), Until: day("2026-08-31"),
		Legend: true, WeekdayLabels: true,
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// 1 month header + 7 weekday rows + blank + legend.
	if len(lines) < 9 {
		t.Fatalf("unexpected shape:\n%s", out)
	}
	if !strings.Contains(lines[0], "Jul") {
		t.Errorf("month header should name the months, got %q", lines[0])
	}
	if !strings.Contains(out, "Mon") || !strings.Contains(out, "Fri") {
		t.Error("weekday labels missing")
	}
	if !strings.Contains(out, "Less") {
		t.Error("legend missing")
	}
	for _, l := range lines {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("row has trailing whitespace: %q", l)
		}
	}
	// The busy day must be shaded darker than the quiet one.
	if !strings.ContainsRune(out, '█') {
		t.Error("the busiest day should use the darkest shade")
	}
}

func TestHeatmapEmptyIsAllBlank(t *testing.T) {
	out := stats.Heatmap(map[string]int{}, stats.HeatmapOptions{
		Since: day("2026-08-01"), Until: day("2026-08-28"),
	})
	if strings.ContainsAny(out, "░▒▓█") {
		t.Errorf("no activity should render no shading:\n%s", out)
	}
}

func TestHumanDuration(t *testing.T) {
	for _, tc := range []struct {
		secs int
		want string
	}{
		{30, "30s"}, {90, "1m"}, {3600, "1h 0m"}, {5400, "1h 30m"}, {90000, "25h"},
	} {
		if got := stats.HumanDuration(tc.secs); got != tc.want {
			t.Errorf("HumanDuration(%d) = %q, want %q", tc.secs, got, tc.want)
		}
	}
}

func TestBackfillGitIsIdempotent(t *testing.T) {
	r := gittest.New(t)
	r.Commit("a.txt", "a\n", "feat: one")
	r.Commit("b.txt", "b\n", "feat: two")

	s := open(t)
	repos := []repo.Repo{{Name: "repo", Path: r.Root, HasGit: true}}
	since := time.Now().AddDate(-1, 0, 0)

	if _, err := stats.BackfillGit(context.Background(), s, repos, since, ""); err != nil {
		t.Fatal(err)
	}
	q := stats.Query{Since: since, Until: time.Now().AddDate(0, 0, 1)}
	first, _ := s.RepoTotals(q)
	if len(first) != 1 {
		t.Fatalf("want one repo, got %+v", first)
	}

	// Running again over the same window must not double the numbers.
	if _, err := stats.BackfillGit(context.Background(), s, repos, since, ""); err != nil {
		t.Fatal(err)
	}
	second, _ := s.RepoTotals(q)
	if second[0].Seconds != first[0].Seconds {
		t.Errorf("backfill is not idempotent: %d then %d", first[0].Seconds, second[0].Seconds)
	}
}

func TestBackfillGitFiltersByAuthor(t *testing.T) {
	r := gittest.New(t)
	r.Commit("a.txt", "a\n", "feat: mine")

	s := open(t)
	repos := []repo.Repo{{Name: "repo", Path: r.Root, HasGit: true}}
	since := time.Now().AddDate(-1, 0, 0)

	if _, err := stats.BackfillGit(context.Background(), s, repos, since, "someone-else@example.test"); err != nil {
		t.Fatal(err)
	}
	if !s.Empty() {
		t.Error("commits by another author should not be counted")
	}
}

func TestAPIKeyFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".wakatime.cfg")
	os.WriteFile(path, []byte("[settings]\ndebug = false\n# a comment\napi_key=waka_secret\n"), 0o600)

	got, err := stats.APIKeyFromConfig(path)
	if err != nil || got != "waka_secret" {
		t.Errorf("APIKeyFromConfig = %q, %v", got, err)
	}

	os.WriteFile(path, []byte("[settings]\ndebug = false\n"), 0o600)
	if _, err := stats.APIKeyFromConfig(path); err == nil {
		t.Error("a config with no api_key should error")
	}
	if _, err := stats.APIKeyFromConfig(filepath.Join(dir, "absent")); err == nil {
		t.Error("a missing config should error")
	}
}

func TestWakaTimeImport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Basic ") {
			t.Errorf("expected Basic auth, got %q", auth)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"range":    map[string]string{"date": "2026-08-01"},
					"projects": []map[string]any{{"name": "api", "total_seconds": 3600.0}},
				},
				{
					"range":    map[string]string{"date": "2026-08-02"},
					"projects": []map[string]any{{"name": "api", "total_seconds": 1800.0}, {"name": "web", "total_seconds": 0.0}},
				},
			},
		})
	}))
	defer srv.Close()

	s := open(t)
	w := &stats.WakaTime{APIKey: "k", BaseURL: srv.URL}
	n, err := w.Import(context.Background(), s, day("2026-08-01"), day("2026-08-31"))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 project-days (the zero-second one is dropped), got %d", n)
	}
	totals, _ := s.RepoTotals(stats.Query{
		Since: day("2026-07-01"), Until: day("2026-09-01"),
		Sources: []stats.Source{stats.SourceWakaTime},
	})
	if len(totals) != 1 || totals[0].Seconds != 5400 {
		t.Errorf("imported totals wrong: %+v", totals)
	}
}

func TestWakaTimeImportRequiresKey(t *testing.T) {
	w := &stats.WakaTime{}
	if _, err := w.Import(context.Background(), open(t), day("2026-08-01"), day("2026-08-02")); err == nil {
		t.Error("importing without an API key should error")
	}
}

func TestCollectorMarks(t *testing.T) {
	s := open(t)
	if !s.LastCollected("git").IsZero() {
		t.Error("a collector that never ran should report the zero time")
	}
	now := time.Now().Truncate(time.Second)
	if err := s.MarkCollected("git", now); err != nil {
		t.Fatal(err)
	}
	if got := s.LastCollected("git"); !got.Equal(now) {
		t.Errorf("LastCollected = %v, want %v", got, now)
	}
}
