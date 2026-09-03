package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

// installFakeGH puts a stub gh on PATH that satisfies the readiness probe and
// answers `pr list` with the supplied rows. Returns the invocation log.
func installFakeGH(t *testing.T, prListJSON string) string {
	t.Helper()
	return installFakeGHResponses(t, prListJSON, "[]")
}

func installFakeGHResponses(t *testing.T, prListJSON, searchJSON string) string {
	t.Helper()
	t.Setenv("GH_HOST", "")
	if runtime.GOOS == "windows" {
		t.Skip("the stub CLI is a POSIX shell script")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	payload := filepath.Join(dir, "prs.json")
	searchPayload := filepath.Join(dir, "search.json")
	if err := os.WriteFile(payload, []byte(prListJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(searchPayload, []byte(searchJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "` + log + `"
case "$*" in
  *'auth status'*) echo "logged in"; exit 0 ;;
  *'pr list'*)     exec cat "` + payload + `" ;;
  *'search prs'*)  exec cat "` + searchPayload + `" ;;
  *)               printf '[]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// A signed-out glab stub shadows any real one, so the test exercises the
	// single-ready-provider path on every machine. PATH keeps its tail so the
	// stubs can still use ordinary shell utilities.
	signedOut := "#!/bin/sh\necho 'glab: not logged in' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(signedOut), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestPRListJoinsAPullRequestToItsWorktree(t *testing.T) {
	h := newHarness(t)
	// A task whose branch matches the request's head branch below.
	h.mustRun("start", "demo", "--task", "auth", "--branch", "feat/auth", "--base", "main")
	// The join keys on the origin identity, so the checkout needs one.
	h.repo.Git("remote", "add", "origin", "https://github.com/acme/demo.git")

	installFakeGH(t, `[{
		"number": 42, "title": "Add auth", "url": "https://github.com/acme/demo/pull/42",
		"state": "OPEN", "isDraft": false, "author": {"login": "me"},
		"headRefName": "feat/auth", "baseRefName": "main",
		"reviewDecision": "APPROVED", "mergeable": "MERGEABLE",
		"statusCheckRollup": [{"status": "COMPLETED", "conclusion": "SUCCESS"}],
		"updatedAt": "2026-09-01T10:00:00Z"
	}]`)

	out := h.mustRun("pr", "list", "--repo", "acme/demo", "--json")
	var payload struct {
		Providers []struct {
			Forge  string `json:"forge"`
			Status string `json:"status"`
		} `json:"providers"`
		PullRequests []struct {
			Repo           string            `json:"repo"`
			Number         int               `json:"number"`
			HeadBranch     string            `json:"head_branch"`
			ReviewDecision string            `json:"review_decision"`
			Checks         string            `json:"checks"`
			Detail         string            `json:"detail"`
			Actions        map[string]string `json:"actions"`
			Local          *struct {
				TaskID           string `json:"task_id"`
				Checkout         string `json:"checkout"`
				ExpectedBranch   string `json:"expected_branch"`
				LiveBranch       string `json:"live_branch"`
				BranchCheckedOut bool   `json:"branch_checked_out"`
				StatusAvailable  bool   `json:"status_available"`
			} `json:"local"`
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("pr list --json is not valid JSON: %v\n%s", err, out)
	}
	if len(payload.PullRequests) != 1 {
		t.Fatalf("want 1 request, got %d:\n%s", len(payload.PullRequests), out)
	}
	pr := payload.PullRequests[0]
	if pr.Number != 42 || pr.HeadBranch != "feat/auth" {
		t.Errorf("unexpected request: %+v", pr)
	}
	if pr.Detail != "full" || pr.ReviewDecision != "approved" || pr.Checks != "passing" {
		t.Errorf("rich fields missing: %+v", pr)
	}
	// This is the whole point of the local scope: knowing which checkout the
	// request belongs to.
	if pr.Local == nil {
		t.Fatalf("request was not joined to its worktree:\n%s", out)
	}
	if pr.Local.ExpectedBranch != "feat/auth" || pr.Local.LiveBranch != "feat/auth" ||
		!pr.Local.BranchCheckedOut || !pr.Local.StatusAvailable ||
		!strings.Contains(pr.Local.Checkout, "feat-auth") {
		t.Errorf("local join: %+v", *pr.Local)
	}
	// Commands are reported, never run.
	if !strings.Contains(pr.Actions["merge"], "gh pr merge 42 --repo acme/demo") {
		t.Errorf("actions: %+v", pr.Actions)
	}
	// A provider that could not be consulted must be visible as a gap.
	if len(payload.Providers) == 0 {
		t.Error("provider statuses were not reported")
	}
}

func TestPRLocalScopeIncludesTaskRepositoryOutsideScanRoots(t *testing.T) {
	h := newHarness(t)
	outside := gittest.New(t)
	outside.Git("remote", "add", "origin", "https://github.com/acme/outside.git")
	h.mustRun("start", outside.Root, "--task", "outside", "--branch", "feat/outside", "--base", "main")
	installFakeGH(t, `[{"number":9,"title":"outside","url":"u","state":"OPEN",
		"author":{"login":"me"},"headRefName":"feat/outside","baseRefName":"main"}]`)

	out := h.mustRun("pr", "list", "--scope", "local", "--json")
	if !strings.Contains(out, `"repo": "acme/outside"`) || !strings.Contains(out, `"branch_checked_out": true`) {
		t.Fatalf("outside task repository was omitted: %s", out)
	}
}

func TestPRListJoinsUnmanagedGitWorktreeWithoutTask(t *testing.T) {
	h := newHarness(t)
	h.repo.Git("remote", "add", "origin", "https://github.com/acme/demo.git")
	h.repo.Git("branch", "feat/unmanaged")
	worktree := filepath.Join(h.home, "external-unmanaged")
	h.repo.Git("worktree", "add", worktree, "feat/unmanaged")
	installFakeGH(t, `[{"number":6,"title":"unmanaged","url":"u","state":"OPEN",
		"author":{"login":"me"},"headRefName":"feat/unmanaged","baseRefName":"main"}]`)

	out := h.mustRun("pr", "list", "--scope", "local", "--repo", "acme/demo", "--json")
	var payload struct {
		PullRequests []struct {
			Local *struct {
				TaskID           string `json:"task_id"`
				Checkout         string `json:"checkout"`
				BranchCheckedOut bool   `json:"branch_checked_out"`
			} `json:"local"`
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.PullRequests) != 1 || payload.PullRequests[0].Local == nil ||
		payload.PullRequests[0].Local.TaskID != "" || !payload.PullRequests[0].Local.BranchCheckedOut {
		t.Fatalf("unmanaged worktree was not joined: %s", out)
	}
	gotInfo, gotErr := os.Stat(payload.PullRequests[0].Local.Checkout)
	wantInfo, wantErr := os.Stat(worktree)
	if gotErr != nil || wantErr != nil || !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("joined checkout %q is not %q", payload.PullRequests[0].Local.Checkout, worktree)
	}
}

func TestPRListJoinsForkRequestToTheSourceRepository(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "fork", "--branch", "feat/fork", "--base", "main")
	h.repo.Git("remote", "add", "origin", "https://github.com/me/fork.git")
	installFakeGH(t, `[{"number":8,"title":"fork PR","url":"u","state":"OPEN",
		"author":{"login":"me"},"headRepository":{"nameWithOwner":"me/fork"},"isCrossRepository":true,
		"headRefName":"feat/fork","baseRefName":"main"}]`)

	out := h.mustRun("pr", "list", "--scope", "local", "--repo", "org/upstream", "--json")
	var payload struct {
		PullRequests []struct {
			HeadRepo string `json:"head_repo"`
			Local    *struct {
				TaskID string `json:"task_id"`
			} `json:"local"`
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.PullRequests) != 1 || payload.PullRequests[0].HeadRepo != "me/fork" ||
		payload.PullRequests[0].Local == nil {
		t.Fatalf("fork source was not joined: %s", out)
	}
}

func TestPRListRepoRestrictsAccountRows(t *testing.T) {
	h := newHarness(t)
	installFakeGHResponses(t, "[]", `[
		{"number":1,"title":"wanted","url":"u1","state":"OPEN","author":{"login":"me"},"repository":{"nameWithOwner":"acme/demo"}},
		{"number":2,"title":"leak","url":"u2","state":"OPEN","author":{"login":"me"},"repository":{"nameWithOwner":"other/repo"}}
	]`)

	out := h.mustRun("pr", "list", "--scope", "account", "--repo", "acme/demo", "--json")
	var payload struct {
		PullRequests []struct {
			Repo string `json:"repo"`
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.PullRequests) != 1 || payload.PullRequests[0].Repo != "acme/demo" {
		t.Fatalf("--repo leaked account rows: %s", out)
	}
}

func TestPRListNormalizesURLAndReportsEffectiveScope(t *testing.T) {
	h := newHarness(t)
	log := installFakeGH(t, `[{"number":3,"title":"merged","url":"u","state":"MERGED","author":{"login":"me"}}]`)

	out := h.mustRun("pr", "list", "--scope", "account", "--state", "merged",
		"--repo", "https://github.com/acme/demo.git", "--json")
	var payload struct {
		Scope        string   `json:"scope"`
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Scope != "local" {
		t.Errorf("effective scope = %q, want local", payload.Scope)
	}
	if len(payload.Repositories) != 1 || payload.Repositories[0] != "github:github.com/acme/demo" {
		t.Errorf("normalized repositories = %v", payload.Repositories)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "--repo acme/demo") || strings.Contains(string(calls), "https://github.com") {
		t.Errorf("URL selector was not normalized before provider call:\n%s", calls)
	}
}

func TestPRListRefusesRemoteOnDifferentConfiguredHost(t *testing.T) {
	h := newHarness(t)
	log := installFakeGH(t, `[{"number":1,"title":"wrong host","state":"OPEN"}]`)
	_, _, err := h.run("pr", "list", "--scope", "local", "--repo", "https://github.corp/acme/demo.git")
	if err == nil || !strings.Contains(err.Error(), "does not match configured host github.com") {
		t.Fatalf("err = %v", err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "pr list") {
		t.Fatalf("mismatched enterprise remote was queried on github.com:\n%s", calls)
	}
}

func TestPRListMissingWorktreeDoesNotLookClean(t *testing.T) {
	h := newHarness(t)
	h.mustRun("start", "demo", "--task", "gone", "--branch", "feat/gone", "--base", "main")
	h.repo.Git("remote", "add", "origin", "https://github.com/acme/demo.git")
	worktree := filepath.Join(h.wtRoot, "demo", "feat-gone")
	h.repo.Git("worktree", "remove", worktree)
	installFakeGH(t, `[{"number":4,"title":"gone","url":"u","state":"OPEN","author":{"login":"me"},"headRefName":"feat/gone"}]`)

	out := h.mustRun("pr", "list", "--scope", "local", "--repo", "acme/demo", "--json")
	var payload struct {
		PullRequests []struct {
			Local *struct {
				CheckoutExists   bool            `json:"checkout_exists"`
				StatusAvailable  bool            `json:"status_available"`
				BranchCheckedOut bool            `json:"branch_checked_out"`
				StatusError      string          `json:"status_error"`
				Git              json.RawMessage `json:"git"`
			} `json:"local"`
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.PullRequests) != 1 || payload.PullRequests[0].Local == nil {
		t.Fatalf("missing task association: %s", out)
	}
	local := payload.PullRequests[0].Local
	if local.CheckoutExists || local.StatusAvailable || local.BranchCheckedOut || len(local.Git) != 0 {
		t.Errorf("missing worktree looks usable: %+v\n%s", *local, out)
	}
	if !strings.Contains(local.StatusError, "missing") {
		t.Errorf("status error = %q", local.StatusError)
	}
}

func TestPRListReportsASignedOutProviderWithoutLeakingArgv(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	// gh is installed but signed out: `auth status` fails the way it really does.
	script := `#!/bin/sh
echo "gh: You are not logged into any GitHub hosts. To log in, run: gh auth login" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, _, err := h.run("pr", "list")
	if err == nil {
		t.Fatal("expected a failure when no provider is authenticated")
	}
	message := err.Error()
	if !strings.Contains(message, "gh auth login") {
		t.Errorf("error is not actionable: %q", message)
	}
	// The reported symptom: the provider's raw command line reaching the user.
	// `--hostname` is deliberately not on this list — it belongs to the login
	// command being recommended, which is the useful half.
	for _, leak := range []string{"exit status", "auth status", "--method", "api "} {
		if strings.Contains(message, leak) {
			t.Errorf("error leaks %q: %q", leak, message)
		}
	}
}

func TestPRListRejectsUnknownFilters(t *testing.T) {
	h := newHarness(t)
	for name, args := range map[string][]string{
		"scope": {"pr", "list", "--scope", "everything"},
		"state": {"pr", "list", "--state", "abandoned"},
		"role":  {"pr", "list", "--role", "bystander"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := h.run(args...)
			if err == nil {
				t.Fatalf("dev %s should have been rejected", strings.Join(args, " "))
			}
			if !strings.Contains(err.Error(), "want ") {
				t.Errorf("error does not say what is valid: %v", err)
			}
		})
	}
}

func TestAgentSectionIsRejectedInProjectConfig(t *testing.T) {
	h := newHarness(t)
	projectDir := filepath.Join(h.repo.Root, ".dev-cli")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A repository must never be able to define a command dev will run.
	body := "version = 1\n\n[[agent]]\nname = \"evil\"\ncommand = [\"sh\", \"-c\", \"echo pwned\"]\n"
	if err := os.WriteFile(filepath.Join(projectDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := h.run("config", "show", "--repo", h.repo.Root)
	if err != nil {
		// Not every build exposes this exact subcommand; the denial itself is
		// covered by the projectconfig package test.
		t.Skipf("config show unavailable: %v (%s)", err, errOut)
	}
	if strings.Contains(out, "pwned") {
		t.Error("a repository-supplied agent command was accepted")
	}
}

func appendConfig(t *testing.T, path, extra string) {
	t.Helper()
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(existing, []byte(extra)...), 0o644); err != nil {
		t.Fatal(err)
	}
}
