package forge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// installScriptedCLI puts a stub provider CLI on PATH that logs its argv and
// answers from a caller-supplied dispatch table keyed by a substring of the
// invocation. It is the same technique as installPagedCLI, generalized past
// that helper's fixed two-page pagination.
func installScriptedCLI(t *testing.T, name string, responses map[string]any) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub CLI is a POSIX shell script")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")

	matches := make([]string, 0, len(responses))
	for match := range responses {
		matches = append(matches, match)
	}
	// Longest first, so a more specific pattern is never shadowed by a shorter
	// one, and so the generated script does not depend on map ordering.
	sort.Slice(matches, func(i, j int) bool {
		if len(matches[i]) != len(matches[j]) {
			return len(matches[i]) > len(matches[j])
		}
		return matches[i] < matches[j]
	})
	var cases []string
	for _, match := range matches {
		body, err := json.Marshal(responses[match])
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(match, "'*?[") {
			t.Fatalf("dispatch key %q would not survive shell quoting", match)
		}
		path := filepath.Join(dir, strings.NewReplacer("/", "_", " ", "_", "=", "_").Replace(match)+".json")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		// The pattern is single-quoted because dispatch keys contain spaces,
		// which would otherwise split the case label.
		cases = append(cases, "  *'"+match+"'*) exec cat "+path+" ;;")
	}
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >> \"$SCRIPTED_CLI_LOG\"\ncase \"$*\" in\n" +
		strings.Join(cases, "\n") + "\n  *) printf '[]' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SCRIPTED_CLI_LOG", log)
	return log
}

func TestGitHubAccountSearchUnionsRolesForOnePullRequest(t *testing.T) {
	// The same request can be both authored by you and awaiting your review;
	// it must appear once, carrying both reasons.
	shared := map[string]any{
		"number": 7, "title": "Add retry", "url": "https://github.com/o/n/pull/7",
		"state": "OPEN", "isDraft": false,
		"author":     map[string]any{"login": "me"},
		"repository": map[string]any{"nameWithOwner": "o/n"},
		"updatedAt":  "2026-09-01T10:00:00Z",
	}
	other := map[string]any{
		"number": 9, "title": "Fix parse", "url": "https://github.com/o/m/pull/9",
		"state": "OPEN", "isDraft": true,
		"author":     map[string]any{"login": "someone"},
		"repository": map[string]any{"nameWithOwner": "o/m"},
		"updatedAt":  "2026-08-30T10:00:00Z",
	}
	log := installScriptedCLI(t, "gh", map[string]any{
		"--author":           []any{shared},
		"--review-requested": []any{shared, other},
	})

	prs, err := (&gh{}).ListAccountPRs(t.Context(), PRQuery{})
	if err != nil {
		t.Fatalf("ListAccountPRs: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("got %d requests, want 2: %+v", len(prs), prs)
	}
	if prs[0].Key() != "github/o/n#7" {
		t.Errorf("first key = %q", prs[0].Key())
	}
	if got := prs[0].Roles; len(got) != 2 || got[0] != RoleAuthor || got[1] != RoleReviewer {
		t.Errorf("roles = %v, want both author and reviewer", got)
	}
	if prs[1].Roles[0] != RoleReviewer {
		t.Errorf("second request roles = %v", prs[1].Roles)
	}
	// The search surface cannot report these, and must not pretend otherwise.
	if prs[0].Detail != PRDetailSummary {
		t.Errorf("detail = %q, want summary", prs[0].Detail)
	}
	if prs[0].HeadBranch != "" || prs[0].ReviewDecision != "" || prs[0].Checks != "" {
		t.Errorf("search surface invented fields it cannot know: %+v", prs[0])
	}

	invocations, _ := os.ReadFile(log)
	for _, want := range []string{"search prs", "--author @me", "--review-requested @me", "--state open"} {
		if !strings.Contains(string(invocations), want) {
			t.Errorf("missing %q in:\n%s", want, invocations)
		}
	}
}

func TestGitHubRepoListReportsBranchReviewAndChecks(t *testing.T) {
	log := installScriptedCLI(t, "gh", map[string]any{
		"pr list": []any{map[string]any{
			"number": 12, "title": "Ship it", "url": "https://github.com/o/n/pull/12",
			"state": "OPEN", "isDraft": false,
			"author":         map[string]any{"login": "me"},
			"headRefName":    "feat/ship",
			"baseRefName":    "main",
			"reviewDecision": "APPROVED",
			"mergeable":      "MERGEABLE",
			"statusCheckRollup": []any{
				map[string]any{"status": "COMPLETED", "conclusion": "SUCCESS"},
				map[string]any{"status": "COMPLETED", "conclusion": "SKIPPED"},
			},
			"updatedAt": "2026-09-01T10:00:00Z",
		}},
	})

	prs, err := (&gh{}).ListRepoPRs(t.Context(), "o/n", PRQuery{})
	if err != nil {
		t.Fatalf("ListRepoPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d requests, want 1", len(prs))
	}
	pr := prs[0]
	if pr.Detail != PRDetailFull {
		t.Errorf("detail = %q, want full", pr.Detail)
	}
	if pr.HeadBranch != "feat/ship" || pr.BaseBranch != "main" {
		t.Errorf("branches = %q -> %q", pr.HeadBranch, pr.BaseBranch)
	}
	if pr.ReviewDecision != "approved" || pr.Checks != ChecksPassing {
		t.Errorf("review = %q, checks = %q", pr.ReviewDecision, pr.Checks)
	}
	if pr.Repo != "o/n" {
		t.Errorf("repo = %q", pr.Repo)
	}
	invocations, _ := os.ReadFile(log)
	if !strings.Contains(string(invocations), "--repo o/n") {
		t.Errorf("repo was not scoped:\n%s", invocations)
	}
}

func TestGitHubAccountSearchRefusesNonOpenStates(t *testing.T) {
	// gh search prs accepts only open|closed, so a merged query must be routed
	// to the per-repository surface rather than silently returning nothing.
	installScriptedCLI(t, "gh", map[string]any{})
	_, err := (&gh{}).ListAccountPRs(t.Context(), PRQuery{State: PRStateMerged})
	var unsupported *ErrUnsupported
	if err == nil || !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestListAccountPRsRefusesAdaptersWithoutTheCapability(t *testing.T) {
	// Azure DevOps declines by not implementing PRLister at all.
	_, err := ListAccountPRs(t.Context(), NewAzureDevOps(nil), PRQuery{})
	var unsupported *ErrUnsupported
	if err == nil || !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestGitLabAccountListParsesReferencesAndBranches(t *testing.T) {
	mr := map[string]any{
		"iid": 3, "title": "Tune cache", "web_url": "https://gitlab.com/g/p/-/merge_requests/3",
		"state": "opened", "draft": false,
		"source_branch": "feat/cache", "target_branch": "main",
		"author":                map[string]any{"username": "me"},
		"references":            map[string]any{"full": "g/p!3"},
		"detailed_merge_status": "mergeable",
		"updated_at":            "2026-09-01T10:00:00.000Z",
	}
	log := installScriptedCLI(t, "glab", map[string]any{
		"scope=created_by_me":            []any{mr},
		"reviewer_username":              []any{},
		"api --hostname gitlab.com user": map[string]any{"username": "me"},
	})

	prs, err := (&glab{}).ListAccountPRs(t.Context(), PRQuery{})
	if err != nil {
		t.Fatalf("ListAccountPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d requests, want 1: %+v", len(prs), prs)
	}
	pr := prs[0]
	// references.full avoids a second call to resolve the project path by id.
	if pr.Repo != "g/p" {
		t.Errorf("repo = %q, want g/p", pr.Repo)
	}
	if pr.Number != 3 || pr.HeadBranch != "feat/cache" || pr.State != PRStateOpen {
		t.Errorf("unexpected request: %+v", pr)
	}
	// GitLab's list surface carries branches, so it is full detail even though
	// it cannot report pipeline status.
	if pr.Detail != PRDetailFull {
		t.Errorf("detail = %q, want full", pr.Detail)
	}
	if pr.Checks != "" {
		t.Errorf("checks = %q, want empty for GitLab", pr.Checks)
	}
	invocations, _ := os.ReadFile(log)
	if !strings.Contains(string(invocations), "reviewer_username=me") {
		t.Errorf("reviewer query did not resolve the username:\n%s", invocations)
	}
}

func TestListPRsReturnsPartialResultsWithTheError(t *testing.T) {
	// A failure on the second role must not discard the first role's rows.
	dir := t.TempDir()
	script := `#!/bin/sh
set -eu
case "$*" in
  *--author*) printf '[{"number":1,"title":"t","url":"u","state":"OPEN","author":{"login":"me"},"repository":{"nameWithOwner":"o/n"}}]' ;;
  *) echo "gh: Bad credentials (HTTP 401)" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prs, err := (&gh{}).ListAccountPRs(t.Context(), PRQuery{})
	if err == nil {
		t.Fatal("expected the reviewer query to fail")
	}
	if !IsAuth(err) {
		t.Errorf("error was not classified as authentication: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("partial results were discarded: %+v", prs)
	}
}

func TestFoldChecksPrefersTheWorstOutcome(t *testing.T) {
	for name, tc := range map[string]struct {
		checks []checkOutcome
		want   string
	}{
		"empty":                {nil, ChecksNone},
		"all green":            {[]checkOutcome{{Status: "COMPLETED", Conclusion: "SUCCESS"}}, ChecksPassing},
		"skipped counts green": {[]checkOutcome{{Status: "COMPLETED", Conclusion: "SKIPPED"}}, ChecksPassing},
		"running":              {[]checkOutcome{{Status: "IN_PROGRESS"}}, ChecksPending},
		"failure wins over pending": {[]checkOutcome{
			{Status: "IN_PROGRESS"}, {Status: "COMPLETED", Conclusion: "FAILURE"},
		}, ChecksFailing},
		"failure wins over success": {[]checkOutcome{
			{Status: "COMPLETED", Conclusion: "SUCCESS"}, {Status: "COMPLETED", Conclusion: "TIMED_OUT"},
		}, ChecksFailing},
		"legacy commit status": {[]checkOutcome{{State: "FAILURE"}}, ChecksFailing},
		"legacy pending":       {[]checkOutcome{{State: "PENDING"}}, ChecksPending},
	} {
		t.Run(name, func(t *testing.T) {
			if got := foldChecks(tc.checks); got != tc.want {
				t.Errorf("foldChecks = %q, want %q", got, tc.want)
			}
		})
	}
}
