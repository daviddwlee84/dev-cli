package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func prTreeRepository() RemoteRow {
	return RemoteRow{Repo: forge.RemoteRepo{Forge: forge.GitHub, FullName: "owner/project", Name: "project", URL: "https://github.com/owner/project"}}
}

func prTreeRow(number int, title string) PRRow {
	return PRRow{PR: forge.PullRequest{Forge: forge.GitHub, Host: "github.com", Repo: "owner/project", Number: number, Title: title, URL: fmt.Sprintf("https://github.com/owner/project/pull/%d", number), State: forge.PRStateOpen, Detail: forge.PRDetailFull}, HeadOID: "head-a"}
}

func prTreeModel(load func(context.Context, PRQuery) (PRResult, error)) Model {
	m := New(Actions{PRs: PRActions{Load: load}}, nil, nil).WithRemotes([]RemoteRow{prTreeRepository()})
	m.view, m.width, m.height = ViewRemote, 120, 40
	return m
}

func prTreeExpand(t *testing.T, m Model) Model {
	t.Helper()
	t.Setenv("GH_HOST", "github.com")
	next, command := m.toggleRemotePRs()
	m = next.(Model)
	if command == nil {
		t.Fatal("first expansion did not schedule a read")
	}
	next, _ = m.Update(command())
	return next.(Model)
}

func TestRemotePRTreeLoadsOnlyOnExplicitExpansionAndPreservesCache(t *testing.T) {
	calls := 0
	m := prTreeModel(func(_ context.Context, query PRQuery) (PRResult, error) {
		calls++
		if query.Limit != 50 || query.Scope != PRScopeAuto || query.Cursor != "" {
			t.Fatalf("query=%+v", query)
		}
		return PRResult{Scope: PRScopeAll, Rows: []PRRow{prTreeRow(1, "selected feature")}}, nil
	})
	_ = m.View()
	next, command := m.afterViewSwitch()
	m = next.(Model)
	if command != nil || calls != 0 {
		t.Fatal("visit or render queried PRs")
	}
	m.filter = "selected"
	_ = m.View()
	if calls != 0 || len(m.visibleRemoteItems()) != 0 {
		t.Fatal("filter queried unknown children")
	}
	m.filter = ""
	original := m
	m = prTreeExpand(t, m)
	if calls != 1 || len(m.visibleRemoteItems()) != 2 || len(original.visibleRemoteItems()) != 1 {
		t.Fatal("lazy expansion or immutable model failed")
	}
	m.remotePRs.repositories[0].result.ObservedAt = time.Now().Add(-10 * time.Minute)
	next, command = m.toggleRemotePRs()
	m = next.(Model)
	if command != nil {
		t.Fatal("collapse queried provider")
	}
	next, command = m.toggleRemotePRs()
	m = next.(Model)
	if command != nil || calls != 1 || !strings.Contains(m.renderDetail(), "stale") {
		t.Fatal("stale cache silently refetched")
	}
	// The ordinary REMOTE refresh affects inventory/metrics, never PR reads.
	_, command = m.runListAction(listActionRefresh)
	if command != nil {
		_ = command()
	}
	if calls != 1 {
		t.Fatal("global refresh queried PRs")
	}
}

func TestRemotePRTreeShowsAdaptiveScopeAndPaginatesWithoutChangingSelection(t *testing.T) {
	calls := 0
	total := 136
	m := prTreeModel(func(_ context.Context, query PRQuery) (PRResult, error) {
		calls++
		if calls == 1 {
			return PRResult{Scope: PRScopeRelated, Total: &total, TotalLowerBound: true, Rows: []PRRow{prTreeRow(1, "first")}, HasMore: true, NextCursor: "page-two"}, nil
		}
		if query.Scope != PRScopeRelated || query.Cursor != "page-two" || query.Limit != 50 {
			t.Fatalf("page query=%+v", query)
		}
		return PRResult{Scope: PRScopeRelated, Total: &total, Rows: []PRRow{prTreeRow(1, "duplicate"), prTreeRow(2, "second")}}, nil
	})
	m = prTreeExpand(t, m)
	if text := m.renderDetail(); !strings.Contains(text, "Related to me") || !strings.Contains(text, "≥136 open overall") || !strings.Contains(text, "show all") {
		t.Fatal(text)
	}
	m.remoteCursor = 1
	key := m.currentToken().key
	next, command := m.runPRListAction(listActionPRMore)
	m = next.(Model)
	if command == nil {
		t.Fatal("pagination not scheduled")
	}
	next, _ = m.Update(command())
	m = next.(Model)
	if m.currentToken().key != key || len(m.visibleRemoteItems()) != 3 || calls != 2 {
		t.Fatal("pagination lost selection or duplicated request")
	}
	if m.remotePRState(prTreeRepository().Repo).result.HasMore {
		t.Fatal("terminal page still marked incomplete")
	}
}

func TestRemotePRTreeRejectsCanceledLateAndForeignResults(t *testing.T) {
	var contextUsed context.Context
	m := prTreeModel(func(ctx context.Context, _ PRQuery) (PRResult, error) {
		contextUsed = ctx
		return PRResult{Scope: PRScopeAll, Rows: []PRRow{prTreeRow(1, "old")}}, nil
	})
	next, command := m.toggleRemotePRs()
	m = next.(Model)
	message := command()
	m.cancelRemotePRs(true)
	if contextUsed.Err() == nil {
		t.Fatal("cancel did not cancel provider context")
	}
	next, _ = m.Update(message)
	m = next.(Model)
	if len(m.visibleRemoteItems()) != 1 {
		t.Fatal("late request revived children")
	}
	foreign := prTreeRow(1, "foreign")
	foreign.PR.Host = "github.other.test"
	m.actions.PRs.Load = func(context.Context, PRQuery) (PRResult, error) {
		return PRResult{Rows: []PRRow{foreign}, Scope: PRScopeAll}, nil
	}
	m = prTreeExpand(t, m)
	if len(m.visibleRemoteItems()) != 1 {
		t.Fatal("foreign PR attached to parent")
	}
}

func TestRemotePRPaginationUnionsRolesButRefreshDropsRevokedRole(t *testing.T) {
	calls := 0
	m := prTreeModel(func(_ context.Context, query PRQuery) (PRResult, error) {
		calls++
		row := prTreeRow(1, "authored request")
		row.PR.Roles = []forge.PRRole{forge.RoleAuthor}
		switch calls {
		case 1:
			return PRResult{Scope: PRScopeRelated, Rows: []PRRow{row}, HasMore: true, NextCursor: "review-page"}, nil
		case 2:
			if query.Cursor != "review-page" {
				t.Fatalf("unexpected continuation: %+v", query)
			}
			row.PR.Title, row.PR.Roles = "new title", []forge.PRRole{forge.RoleReviewer}
		case 3:
			if query.Cursor != "" {
				t.Fatalf("refresh retained continuation: %+v", query)
			}
		default:
			t.Fatalf("unexpected request %d", calls)
		}
		return PRResult{Scope: PRScopeRelated, Rows: []PRRow{row}}, nil
	})
	m = prTreeExpand(t, m)
	m.remoteCursor = 1
	next, command := m.runPRListAction(listActionPRMore)
	m = next.(Model)
	next, _ = m.Update(command())
	m = next.(Model)
	item, _ := m.currentPR()
	if len(m.visibleRemoteItems()) != 2 || item.PR.PR.Title != "new title" || len(item.PR.PR.Roles) != 2 || remotePRRelationship(*item.PR) != "mine · review requested" {
		t.Fatalf("duplicate role page lost badges or latest fields: %+v", item.PR)
	}
	next, command = m.runPRListAction(listActionPRRefresh)
	m = next.(Model)
	next, _ = m.Update(command())
	m = next.(Model)
	item, _ = m.currentPR()
	if calls != 3 || len(item.PR.PR.Roles) != 1 || item.PR.PR.Roles[0] != forge.RoleAuthor || strings.Contains(remotePRRelationship(*item.PR), "review requested") {
		t.Fatalf("full refresh retained revoked review relationship: %+v", item.PR)
	}
}

func TestRemotePRTreePartialFailureRetainsRowsAndFilterExpansionIsTemporary(t *testing.T) {
	m := prTreeModel(func(context.Context, PRQuery) (PRResult, error) {
		return PRResult{Scope: PRScopeAll, Rows: []PRRow{prTreeRow(1, "needle")}}, nil
	})
	m = prTreeExpand(t, m)
	m.actions.PRs.Load = func(context.Context, PRQuery) (PRResult, error) {
		return PRResult{Scope: PRScopeRelated, Rows: []PRRow{prTreeRow(2, "partial")}}, errors.New("rate limited")
	}
	next, command := m.runPRListAction(listActionPRRefresh)
	m = next.(Model)
	next, _ = m.Update(command())
	m = next.(Model)
	if len(m.visibleRemoteItems()) != 2 || !strings.Contains(m.renderDetail(), "rate limited") {
		t.Fatal("refresh failure erased prior rows")
	}
	next, _ = m.toggleRemotePRs()
	m = next.(Model)
	m.filter = "needle"
	if len(m.visibleRemoteItems()) != 2 {
		t.Fatal("search failed to reveal loaded child")
	}
	m.remoteCursor = 1
	next, command = m.toggleRemotePRs()
	m = next.(Model)
	if command != nil || len(m.visibleRemoteItems()) != 1 || m.remoteCursor != 0 {
		t.Fatal("filtered child collapse failed")
	}
	m.filter = ""
	if len(m.visibleRemoteItems()) != 1 {
		t.Fatal("filter collapse altered saved expansion")
	}
}

type prTestWorkflow struct{ result WorkflowResult }

func (*prTestWorkflow) Run() error               { return nil }
func (*prTestWorkflow) SetStdin(io.Reader)       {}
func (*prTestWorkflow) SetStdout(io.Writer)      {}
func (*prTestWorkflow) SetStderr(io.Writer)      {}
func (w *prTestWorkflow) Result() WorkflowResult { return w.result }

func TestRemotePRActionsUseExactChildAndFreshDetailPatch(t *testing.T) {
	m := prTreeModel(func(context.Context, PRQuery) (PRResult, error) {
		return PRResult{Scope: PRScopeAll, Rows: []PRRow{prTreeRow(1, "feature")}}, nil
	})
	m = prTreeExpand(t, m)
	m.remoteCursor = 1
	var selected PRActionRequest
	m.actions.PRs.Run = func(_ context.Context, request PRActionRequest) (Workflow, error) {
		selected = request
		return &prTestWorkflow{}, nil
	}
	if _, ok := m.currentRemote(); ok {
		t.Fatal("PR acquired parent repository actions")
	}
	if availability := func() ActionAvailability {
		spec, _ := findAction(ViewRemote, listActionRemoteClone)
		return spec.availability(m.actionContext())
	}(); availability.Applicable {
		t.Fatal("PR offered repository clone action")
	}
	spec, _ := findAction(ViewRemote, listActionPRProvision)
	if availability := spec.availability(m.actionContext()); availability.Reason == "" {
		t.Fatal("provision accepted no PR checkout")
	}
	_, command := m.runListAction(listActionOpen)
	if command == nil || selected.Action != PRCheckout || selected.Row.PR.Number != 1 || selected.Repository.FullName != "owner/project" {
		t.Fatal("Enter did not target exact PR checkout")
	}
	patch := *m.visibleRemoteItems()[1].PR
	patch.Readiness, patch.HeadOID = "ready", "head-b"
	next, _ := m.Update(remotePRActionMsg{key: remotePRRepositoryKey(prTreeRepository().Repo), request: selected, result: WorkflowResult{Scoped: true, PR: &patch}})
	m = next.(Model)
	if row, _ := m.currentPR(); row.PR.HeadOID != "head-b" || !strings.Contains(m.renderDetail(), "ready") {
		t.Fatal("explicit detail result did not update selected row")
	}
}

func TestRemotePRTreeRefreshPreservesChildAndSnippetSelection(t *testing.T) {
	m := prTreeModel(func(context.Context, PRQuery) (PRResult, error) {
		return PRResult{Scope: PRScopeAll, Rows: []PRRow{prTreeRow(1, "feature")}}, nil
	})
	m = prTreeExpand(t, m)
	m.remoteCursor = 1
	key := m.currentToken().key
	repository := prTreeRepository()
	other := RemoteRow{Repo: forge.RemoteRepo{Forge: forge.GitHub, FullName: "a/first", URL: "https://github.com/a/first", UpdatedAt: time.Now()}}
	generation := m.beginViewLoad(ViewRemote, loadRefresh)
	next, _ := m.Update(remoteMsg{generation: generation, valid: true, rows: []RemoteRow{other, repository}})
	m = next.(Model)
	if m.currentToken().key != key {
		t.Fatal("repository refresh moved selected child")
	}
	m.snippets.enabled, m.snippets.cursor = true, 0
	m.snippets.result.Rows = []SnippetRow{{Key: "snippet", Title: "kept"}}
	if _, ok := m.currentPR(); ok {
		t.Fatal("repository PRs leaked into snippets")
	}
	if !strings.Contains(m.currentToken().key, "snippet") {
		t.Fatal("snippet selection changed")
	}
	m.snippets.enabled = false
	if m.currentToken().key != key {
		t.Fatal("return from snippets lost PR selection")
	}
}

func TestRemotePRCheckoutPatchKeepsRemoteReadinessStale(t *testing.T) {
	m := prTreeModel(func(context.Context, PRQuery) (PRResult, error) {
		return PRResult{Scope: PRScopeAll, Rows: []PRRow{prTreeRow(1, "feature")}}, nil
	})
	m = prTreeExpand(t, m)
	m.remoteCursor = 1
	item, _ := m.currentPR()
	patch := *item.PR
	patch.LocalPath, patch.Readiness, patch.Stale = "/confirmed/local/checkout", "ready", true
	request := PRActionRequest{Action: PRCheckout, Repository: item.Repository.Repo, Row: *item.PR}
	next, _ := m.Update(remotePRActionMsg{key: remotePRRepositoryKey(request.Repository), request: request, result: WorkflowResult{Scoped: true, PR: &patch}})
	m = next.(Model)
	selected, _ := m.currentPR()
	if selected.PR.LocalPath != patch.LocalPath || !selected.PR.Stale || m.remotePRDetailFresh(*selected.PR) || !selected.PR.ObservedAt.IsZero() {
		t.Fatalf("local checkout evidence refreshed remote readiness: %+v", selected.PR)
	}
	if !strings.Contains(m.renderDetail(), "stale observation") {
		t.Fatal("cached readiness appeared current")
	}
}

func TestGHDashNativeActionEligibilityAndOptionalCapability(t *testing.T) {
	m := prTreeModel(nil)
	if target, ok := m.ghDashTarget(); !ok || target.Path != "" || target.Repository.FullName != "owner/project" {
		t.Fatal("uncloned GitHub repository unavailable")
	}
	m.actions.GHDash = GHDashActions{Probe: func(context.Context) bool { t.Fatal("render or action predicate probed gh-dash"); return false }, Prepare: func(context.Context, GHDashRequest) (Workflow, error) { return &prTestWorkflow{}, nil }}
	_ = m.View()
	if reason := m.ghDashReason(); !strings.Contains(reason, "still being checked") {
		t.Fatal(reason)
	}
	m.ghDashAvailability = ToolUnavailable
	if reason := m.ghDashReason(); !strings.Contains(reason, "unavailable") {
		t.Fatal(reason)
	}
	m.ghDashAvailability = ToolAvailable
	if reason := m.ghDashReason(); reason != "" {
		t.Fatal(reason)
	}
	m.remotes[0].Repo.Forge, m.remotes[0].Repo.URL = forge.GitLab, "https://gitlab.com/owner/project"
	if _, ok := m.ghDashTarget(); ok {
		t.Fatal("GitLab offered GitHub tool")
	}
	m.view = ViewRepos
	m.repos = []RepoRow{{Repo: repo.Repo{Path: "/repo"}, Status: gitx.Status{Branch: "main"}, Topology: gitx.RecoveryTopology{Remotes: []gitx.RemoteInfo{{Name: "origin", FetchURLs: []string{"https://github.com/owner/project.git"}}}}}}
	if target, ok := m.ghDashTarget(); !ok || !target.ResolveLocal || target.Path != "/repo" || target.Repository.URL != "https://github.com/owner/project" {
		t.Fatalf("local target=%+v, available=%v", target, ok)
	}
}
