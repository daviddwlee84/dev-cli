package forge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type issueRunnerFunc func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error)

func (f issueRunnerFunc) Run(ctx context.Context, r sshhost.RunRequest) (sshhost.RunResult, error) {
	return f(ctx, r)
}
func TestIssuePublicationUsesStructuredStdin(t *testing.T) {
	target, _ := ParseIssueTarget("")
	body := "literal `command` $(command) \"quote\"\nsecond line"
	reporter := GitHubIssueReporter{Runner: issueRunnerFunc(func(ctx context.Context, r sshhost.RunRequest) (sshhost.RunResult, error) {
		joined := strings.Join(r.Args, " ")
		if strings.Contains(joined, body) || !strings.Contains(joined, "--input -") || !strings.Contains(joined, "--hostname github.com") {
			t.Fatal("body entered command args", joined)
		}
		var data map[string]string
		if json.Unmarshal(r.Stdin, &data) != nil || data["body"] != body || data["title"] != "title" {
			t.Fatal("literal body changed")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("request unbounded")
		}
		return sshhost.RunResult{Stdout: []byte(`{"html_url":"https://github.com/daviddwlee84/dev-cli/issues/42"}`)}, nil
	})}
	url, err := reporter.Publish(context.Background(), IssuePublication{Target: target, Title: "title", Body: body})
	if err != nil || !strings.HasSuffix(url, "/42") {
		t.Fatal(url, err)
	}
}
func TestIssueErrorsAndResponseIdentity(t *testing.T) {
	target, _ := ParseIssueTarget("")
	for _, tt := range []struct {
		message, code string
		unknown       bool
	}{{"gh auth login secret", "unauthenticated", false}, {"HTTP 403 secret", "permission_denied", false}, {"HTTP 422 secret", "request_rejected", false}, {"timeout secret", "request_failed", true}} {
		reporter := GitHubIssueReporter{Runner: issueRunnerFunc(func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error) {
			return sshhost.RunResult{ExitCode: 1, Stderr: []byte(tt.message)}, nil
		})}
		_, err := reporter.Publish(context.Background(), IssuePublication{Target: target})
		var typed *IssueError
		if !errors.As(err, &typed) || typed.Code != tt.code || typed.Unknown != tt.unknown || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
	reporter := GitHubIssueReporter{Runner: issueRunnerFunc(func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error) {
		return sshhost.RunResult{Stdout: []byte(`{"html_url":"https://evil.example/issues/42"}`)}, nil
	})}
	_, err := reporter.Publish(context.Background(), IssuePublication{Target: target})
	var typed *IssueError
	if !errors.As(err, &typed) || !typed.Unknown {
		t.Fatal("untrusted confirmation URL accepted", err)
	}
}
func TestIssueMarkerReconciliationReadsWithoutPublishing(t *testing.T) {
	target, _ := ParseIssueTarget("")
	calls := 0
	reporter := GitHubIssueReporter{Runner: issueRunnerFunc(func(_ context.Context, r sshhost.RunRequest) (sshhost.RunResult, error) {
		calls++
		if strings.Contains(strings.Join(r.Args, " "), "POST") {
			t.Fatal("reconcile published")
		}
		return sshhost.RunResult{Stdout: []byte(`[{"html_url":"https://github.com/daviddwlee84/dev-cli/issues/12","body":"<!-- dev-feedback:marker -->"}]`)}, nil
	})}
	got, err := reporter.FindMarker(context.Background(), target, 0, "<!-- dev-feedback:marker -->")
	if err != nil || !strings.HasSuffix(got, "/12") || calls != 1 {
		t.Fatal(got, err)
	}
}

func TestIssueRepositoryIdentityIsCaseInsensitive(t *testing.T) {
	a, err := ParseIssueTarget("GitHub.com/DavidDWLee84/DEV-CLI")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ParseIssueTarget(DefaultIssueRepository)
	if a != b || !validIssueURL("https://github.com/DavidDWLee84/dev-cli/issues/42", b, 42) {
		t.Fatal(a, b)
	}
}
