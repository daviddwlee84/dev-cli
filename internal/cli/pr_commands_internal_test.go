package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
	"github.com/daviddwlee84/dev-cli/internal/prflow"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type prCommandProvider struct {
	queries []forge.PRPageQuery
	pages   []forge.PRPage
	details int
	detail  forge.PRDetailResult
	diff    forge.PRDiff
	merges  int
	err     error
}

func (p *prCommandProvider) ListPage(_ context.Context, q forge.PRPageQuery) (forge.PRPage, error) {
	p.queries = append(p.queries, q)
	if len(p.pages) == 0 {
		return forge.PRPage{}, p.err
	}
	page := p.pages[0]
	p.pages = p.pages[1:]
	return page, p.err
}
func (p *prCommandProvider) Detail(context.Context, forge.PRReference) (forge.PRDetailResult, error) {
	p.details++
	return p.detail, p.err
}
func (p *prCommandProvider) Diff(context.Context, forge.PRDetailResult) (forge.PRDiff, error) {
	return p.diff, p.err
}
func (p *prCommandProvider) Merge(context.Context, forge.PRDetailResult) (forge.PRMergeOutcome, error) {
	p.merges++
	return forge.PRMergeOutcome{Status: "unknown"}, p.err
}

func prCommandFixture(t *testing.T) (*App, *prCommandProvider, *bytes.Buffer) {
	t.Helper()
	t.Setenv("GH_HOST", "github.com")
	t.Setenv("GITLAB_HOST", "gitlab.com")
	t.Setenv("GLAB_HOST", "")
	provider := &prCommandProvider{}
	provider.detail = forge.PRDetailResult{PullRequest: forge.PullRequest{Forge: forge.GitHub, Host: "github.com", Repo: "acme/api", Number: 12, Title: "Change", URL: "https://github.com/acme/api/pull/12", State: forge.PRStateOpen, HeadBranch: "feature", BaseBranch: "main"}, Reference: forge.PRReference{Forge: forge.GitHub, Host: "github.com", Repo: "acme/api", Number: 12}, AccountID: "viewer", RepositoryID: "repo-id", HeadOID: strings.Repeat("a", 40), BaseOID: strings.Repeat("b", 40), BaseURL: "https://github.com/acme/api.git", HeadURL: "https://github.com/acme/api.git", ObservedAt: time.Now(), Readiness: "ready", CanMerge: true, SquashAllowed: true}
	output := &bytes.Buffer{}
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	cfg.Paths.ScanRoots = []string{t.TempDir()}
	cfg.Paths.RepoPaths = nil
	app := &App{Cfg: cfg, Out: output, Err: io.Discard, In: strings.NewReader(""), interactiveCheck: func() bool { return false }, prProvider: provider}
	return app, provider, output
}

func TestPRViewPreservesUnknownAndSanitizesTerminal(t *testing.T) {
	app, p, out := prCommandFixture(t)
	p.detail.Title = "Change\x1b[31m red\x1b[0m\rspoof"
	p.detail.Readiness = "unknown"
	p.detail.Reason = "checks unavailable"
	cmd := newPRCmd(app)
	cmd.SetArgs([]string{"view", p.detail.Reference.URL()})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "\x1b") || strings.Contains(text, "\r") || !strings.Contains(text, "checks: unknown") || !strings.Contains(text, "? files") || !strings.Contains(text, "merge: unknown") {
		t.Fatalf("unexpected detail: %s", text)
	}
	if p.details != 1 || p.merges != 0 || len(p.queries) != 0 {
		t.Fatalf("unexpected calls: %+v", p)
	}
}

func TestPRDiffNonTTYOutputsPatchWithoutPager(t *testing.T) {
	app, p, out := prCommandFixture(t)
	p.diff = forge.PRDiff{Text: "diff --git a/a b/a\n+text\n", TerminalText: "preview", Complete: true, Live: true}
	cmd := newPRCmd(app)
	cmd.SetArgs([]string{"diff", p.detail.Reference.URL()})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != p.diff.Text {
		t.Fatalf("patch changed: %q", out.String())
	}
}

func TestPRDiffRejectsIncompletePatch(t *testing.T) {
	app, p, out := prCommandFixture(t)
	p.diff = forge.PRDiff{Text: "partial"}
	cmd := newPRCmd(app)
	cmd.SetArgs([]string{"diff", p.detail.Reference.URL(), "--no-pager"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("err=%v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("published incomplete diff: %s", out.String())
	}
}

func TestPRMergeRequiresExplicitStrategyAndNonTTYApproval(t *testing.T) {
	for _, args := range [][]string{{"merge", "https://github.com/acme/api/pull/12"}, {"merge", "https://github.com/acme/api/pull/12", "--squash"}} {
		app, p, _ := prCommandFixture(t)
		cmd := newPRCmd(app)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatal("unapproved merge accepted")
		}
		if p.merges != 0 {
			t.Fatal("provider write happened before approval")
		}
	}
}

func TestPRMergeDryRunIsOneJSONWithoutMutation(t *testing.T) {
	app, p, out := prCommandFixture(t)
	cmd := newPRCmd(app)
	cmd.SetArgs([]string{"merge", p.detail.Reference.URL(), "--squash", "--sync-base", "ff-only", "--dry-run", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Merge    prflow.MergePlan `json:"merge"`
		SyncBase string           `json:"sync_base"`
	}
	dec := json.NewDecoder(out)
	if err := dec.Decode(&report); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("extra output: %v", err)
	}
	if report.Merge.Detail.HeadOID != p.detail.HeadOID || report.SyncBase != "ff-only" || p.merges != 0 {
		t.Fatalf("bad plan: %+v calls %d", report, p.merges)
	}
}

func TestPRRepoListKeepsJSONContractAndPagination(t *testing.T) {
	app, p, out := prCommandFixture(t)
	n := 75
	request := p.detail.PullRequest
	request.HeadBranch = "" // no local discovery needed for a summary
	p.pages = []forge.PRPage{{PullRequests: []forge.PullRequest{request}, Total: &n, Scope: "all", NextCursor: "next", Complete: false}}
	cmd := newPRCmd(app)
	cmd.SetArgs([]string{"list", "--scope", "repo", "--repo", "github:acme/api", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var report struct {
		SchemaVersion int                 `json:"schema_version"`
		Scope         string              `json:"scope"`
		Roles         []string            `json:"roles"`
		PRs           []forge.PullRequest `json:"pull_requests"`
		Pagination    struct {
			Next string `json:"next_cursor"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.Scope != "repo" || len(report.Roles) != 1 || report.Roles[0] != "all" || len(report.PRs) != 1 || report.Pagination.Next != "next" {
		t.Fatalf("bad contract: %s", out.String())
	}
	if len(p.queries) != 1 || p.queries[0].Reference.Repo != "acme/api" || p.details != 0 {
		t.Fatalf("unexpected query fanout: %+v", p)
	}
}

func TestRemotePRAdapterDefersReadsAndScopesLargeRepo(t *testing.T) {
	app, p, _ := prCommandFixture(t)
	total := 105
	p.pages = []forge.PRPage{{PullRequests: []forge.PullRequest{}, Scope: "related", Complete: true}}
	actions := newTUIPRActions(func() *App { return app })
	if len(p.queries) != 0 || p.details != 0 {
		t.Fatal("callback assembly queried provider")
	}
	remote := forge.RemoteRepo{Forge: forge.GitHub, FullName: "acme/api", URL: "https://github.com/acme/api", Metrics: &forgemetrics.Metrics{OpenPRs: forgemetrics.Known(int64(total), time.Now())}}
	result, err := actions.Load(context.Background(), tui.PRQuery{Repository: remote, Scope: tui.PRScopeAuto, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.queries) != 1 || p.queries[0].Relationship != "related" || result.Scope != tui.PRScopeRelated || result.Total == nil || *result.Total != 105 {
		t.Fatalf("unexpected load: %+v %+v", p.queries, result)
	}
}

func TestRemotePRAdapterUnknownCountOnlyQueriesSelectedRepo(t *testing.T) {
	app, p, _ := prCommandFixture(t)
	n := 80
	p.pages = []forge.PRPage{{PullRequests: []forge.PullRequest{}, Total: &n, Scope: "all", NextCursor: "more"}, {PullRequests: []forge.PullRequest{}, Scope: "related", Complete: true}}
	actions := newTUIPRActions(func() *App { return app })
	remote := forge.RemoteRepo{Forge: forge.GitHub, FullName: "acme/api", URL: "https://github.com/acme/api"}
	result, err := actions.Load(context.Background(), tui.PRQuery{Repository: remote, Scope: tui.PRScopeAuto, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.queries) != 2 || p.queries[0].Relationship != "all" || p.queries[1].Relationship != "related" || result.Scope != tui.PRScopeRelated {
		t.Fatalf("queries=%+v result=%+v", p.queries, result)
	}
	for _, q := range p.queries {
		if q.Reference.Repo != "acme/api" {
			t.Fatal("queried another repo")
		}
	}
}

func TestPRCheckoutRejectsConflictingModesBeforeProviderRead(t *testing.T) {
	app, p, _ := prCommandFixture(t)
	cmd := newPRCmd(app)
	cmd.SetArgs([]string{"checkout", p.detail.Reference.URL(), "--try", "--clone", "--path", filepath.Join(t.TempDir(), "clone")})
	if err := cmd.Execute(); err == nil {
		t.Fatal("accepted conflicting modes")
	}
	if p.details != 0 {
		t.Fatal("queried provider for invalid command")
	}
}

func TestPRProvisionCannotRedirectToAnotherCheckout(t *testing.T) {
	app, provider, _ := prCommandFixture(t)
	fixture := newStartFixture(t, runtime.None{})
	fixture.app.prProvider = provider
	fixture.app.interactiveCheck = app.interactiveCheck
	fixture.app.In = strings.NewReader("")
	fixture.repo.Git("remote", "add", "origin", "https://github.com/acme/api.git")
	fixture.repo.Git("branch", "feature")
	fixture.repo.Git("config", "branch.feature.remote", "origin")
	fixture.repo.Git("config", "branch.feature.merge", "refs/heads/feature")
	other := filepath.Join(t.TempDir(), "feature")
	fixture.repo.Git("worktree", "add", other, "feature")
	provider.detail.HeadRepo = "acme/api"
	provider.detail.HeadOID = fixture.repo.Git("rev-parse", "feature")
	provider.detail.BaseOID = fixture.repo.Git("rev-parse", "main")
	_, err := runPRCheckout(t.Context(), fixture.app, provider.detail.Reference, prCheckoutFlags{Repo: fixture.repo.Root, RequiredCheckout: fixture.repo.Root, Provision: true, NoOpen: true})
	if err == nil || !strings.Contains(err.Error(), "selected PR checkout changed") {
		t.Fatalf("provision redirected to another checkout: %v", err)
	}
	if got := fixture.repo.Git("branch", "--show-current"); got != "main" {
		t.Fatalf("canonical branch changed: %s", got)
	}
}

func TestPRMutationObservationHonorsNoRuntime(t *testing.T) {
	app, _, _ := prCommandFixture(t)
	app.noRuntime = true
	if got := prSyncRuntimes(app); len(got) != 0 {
		t.Fatalf("no-runtime still observes multiplexers: %+v", got)
	}
	app.noRuntime = false
	app.runtimeOverride = "none"
	if got := prSyncRuntimes(app); len(got) != 0 {
		t.Fatalf("explicit none still observes multiplexers: %+v", got)
	}
}
