package summary_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/summary"
)

func TestBuildClassifiesAndFiltersAttention(t *testing.T) {
	projects := []summary.Project{
		{Name: "active", Kind: "repository", Present: true, Checkouts: []summary.Checkout{{Exists: true, Git: &summary.Git{Dirty: true}}}},
		{Name: "risk", Kind: "repository", Present: true, Recovery: &summary.Recovery{NoRemote: true}},
		{Name: "quiet", Kind: "repository", Present: true},
	}
	report := summary.Build("host", projects, summary.Capabilities{RuntimeCollected: true}, nil, summary.Options{Attention: true})
	if len(report.Projects) != 2 || report.Totals.Attention != 2 || report.Totals.Dirty != 1 || report.Totals.NoRemote != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !report.Projects[0].Active || report.Projects[1].Active {
		t.Fatalf("active ordering = %+v", report.Projects)
	}
}

func TestAutoRendersActiveDetailAndQuietIndex(t *testing.T) {
	now := time.Now()
	report := summary.Build("host", []summary.Project{
		{Name: "active", Kind: "repository", DisplayPath: "~/src/active", Present: true,
			LatestActivity: now, Tasks: []summary.Task{{ID: "active__feat", State: "hot", Next: "test it"}}},
		{Name: "quiet", Kind: "repository", Present: true, RecentCommits: []summary.Commit{{Subject: "docs: old"}}},
	}, summary.Capabilities{RuntimeCollected: true}, nil, summary.Options{})
	var out bytes.Buffer
	summary.RenderMarkdown(&out, report, summary.DetailAuto, false)
	text := out.String()
	for _, want := range []string{"Active work", "### active", "next: test it", "Project index", "**quiet**"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
}

func TestQueryMatchesTaskAndMetadata(t *testing.T) {
	report := summary.Build("host", []summary.Project{{
		Name: "api", Kind: "repository", Tags: []string{"important"}, Tasks: []summary.Task{{Title: "token refresh"}},
	}}, summary.Capabilities{}, nil, summary.Options{Query: "important token"})
	if len(report.Projects) != 1 {
		t.Fatalf("projects = %+v", report.Projects)
	}
}
