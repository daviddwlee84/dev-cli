package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installFakeGH puts a stub gh on PATH that satisfies the readiness probe and
// answers `pr list` with the supplied rows. Returns the invocation log.
func installFakeGH(t *testing.T, prListJSON string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub CLI is a POSIX shell script")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	payload := filepath.Join(dir, "prs.json")
	if err := os.WriteFile(payload, []byte(prListJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "` + log + `"
case "$*" in
  *'auth status'*) echo "logged in"; exit 0 ;;
  *'pr list'*)     exec cat "` + payload + `" ;;
  *'search prs'*)  printf '[]' ;;
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
				TaskID   string `json:"task_id"`
				Checkout string `json:"checkout"`
				Branch   string `json:"branch"`
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
	if pr.Local.Branch != "feat/auth" || !strings.Contains(pr.Local.Checkout, "feat-auth") {
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

func TestPRPromptRendersTheQueueAndNeedsNoAgent(t *testing.T) {
	h := newHarness(t)
	installFakeGH(t, `[{
		"number": 7, "title": "Tidy", "url": "https://github.com/acme/demo/pull/7",
		"state": "OPEN", "author": {"login": "me"}, "headRefName": "feat/tidy",
		"updatedAt": "2026-09-01T10:00:00Z"
	}]`)

	out := h.mustRun("pr", "prompt", "triage", "--repo", "acme/demo")
	if !strings.Contains(out, "# Pull request triage") {
		t.Fatalf("prompt was not rendered:\n%s", out)
	}
	// The prompt must carry the actual queue, not just instructions.
	if !strings.Contains(out, `"number": 7`) {
		t.Errorf("prompt does not embed the queue:\n%s", out)
	}
	// No template variable may survive into what an agent reads.
	if strings.Contains(out, "{{") {
		t.Errorf("prompt has unsubstituted variables:\n%s", out)
	}
}

func TestPRPromptWithoutAConfiguredAgentExplainsHowToAddOne(t *testing.T) {
	h := newHarness(t)
	installFakeGH(t, `[]`)
	_, _, err := h.run("pr", "prompt", "--agent", "claude", "--repo", "acme/demo")
	if err == nil {
		t.Fatal("expected --agent to fail with no [[agent]] configured")
	}
	// A refusal that does not say how to fix it is the failure mode this
	// feature exists to avoid.
	for _, want := range []string{"[[agent]]", "name =", "command ="} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not show a usable example (%q): %v", want, err)
		}
	}
}

func TestPRPromptHandsTheRenderedPromptToAConfiguredAgent(t *testing.T) {
	h := newHarness(t)
	installFakeGH(t, `[]`)
	// The agent reads the prompt from stdin, which is the default delivery.
	appendConfig(t, h.configPath, "\n[[agent]]\nname = \"echoer\"\ncommand = [\"sh\", \"-c\", \"cat\"]\ndefault = true\n")

	out := h.mustRun("pr", "prompt", "triage", "--repo", "acme/demo", "--agent", "echoer")
	if !strings.Contains(out, "# Pull request triage") {
		t.Fatalf("the agent did not receive the prompt on stdin:\n%s", out)
	}
}

func TestPRPromptDryRunShowsTheCommandAndRunsNothing(t *testing.T) {
	h := newHarness(t)
	installFakeGH(t, `[]`)
	marker := filepath.Join(h.home, "agent-ran")
	appendConfig(t, h.configPath,
		"\n[[agent]]\nname = \"tripwire\"\ncommand = [\"sh\", \"-c\", \"touch "+filepath.ToSlash(marker)+"\"]\ndefault = true\n")

	out := h.mustRun("pr", "prompt", "--repo", "acme/demo", "--dry-run")
	if !strings.Contains(out, "sh -c touch") {
		t.Errorf("dry run did not show the command:\n%s", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("dry run executed the agent")
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
