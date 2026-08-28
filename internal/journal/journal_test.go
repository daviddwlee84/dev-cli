package journal_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/journal"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func TestParseWindowUsesInclusiveCalendarDays(t *testing.T) {
	loc := time.FixedZone("test", 8*60*60)
	now := time.Date(2026, 8, 28, 17, 0, 0, 0, loc)
	since, until, err := journal.ParseWindow("7d", "today", now)
	if err != nil {
		t.Fatal(err)
	}
	if got := since.Format("2006-01-02 15:04"); got != "2026-08-22 00:00" {
		t.Fatalf("since = %s", got)
	}
	if got := until.Format("2006-01-02 15:04"); got != "2026-08-29 00:00" {
		t.Fatalf("until = %s", got)
	}
}

func TestCollectTruncatesDetailsButKeepsTotalsAndMetrics(t *testing.T) {
	r := gittest.New(t)
	r.Commit("one.txt", "one\n", "feat: one")
	r.Commit("two.txt", "two\n", "feat: two")
	discovered, err := gitx.Discover(context.Background(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	end := start.AddDate(0, 0, 1)
	report, err := journal.Collect(context.Background(), []repo.Repo{{
		Name: "repo", Path: r.Root, RealPath: r.Root, CommonDir: discovered.GitCommonDir, HasGit: true,
	}}, journal.Options{
		Since: start, Until: end, Authors: []string{"dev@example.test"},
		Granularity: journal.GranularityAuto, MaxCommits: 1, Metrics: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Commits != 3 || report.Summary.ShownCommits != 1 || report.Summary.OmittedCommits != 2 {
		t.Fatalf("summary = %+v", report.Summary)
	}
	if report.Summary.Metrics.Additions == 0 || len(report.Repositories) != 1 {
		t.Fatalf("metrics/repos = %+v", report)
	}
	var out bytes.Buffer
	journal.RenderMarkdown(&out, report, true)
	if !strings.Contains(out.String(), "2 omitted") || !strings.Contains(out.String(), "feat: two") {
		t.Fatalf("markdown:\n%s", out.String())
	}
}

func TestOtherAuthorDoesNotMatch(t *testing.T) {
	r := gittest.New(t)
	start, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	report, err := journal.Collect(context.Background(), []repo.Repo{{Name: "repo", Path: r.Root, HasGit: true}}, journal.Options{
		Since: start, Until: start.AddDate(0, 0, 1), Authors: []string{"other@example.test"},
		Granularity: journal.GranularityCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Commits != 0 || len(report.Repositories) != 0 {
		t.Fatalf("report = %+v", report)
	}
}
