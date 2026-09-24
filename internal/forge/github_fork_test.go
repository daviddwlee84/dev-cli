package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type forkTestReply struct {
	status int
	body   string
	err    error
}

type forkTestProvider struct {
	t       *testing.T
	replies map[string][]forkTestReply
	calls   [][]string
	creates int
	create  func() (string, error)
}

func installForkTestProvider(t *testing.T) *forkTestProvider {
	t.Helper()
	p := &forkTestProvider{t: t, replies: map[string][]forkTestReply{}}
	originalLookup, originalRunner := lookupPath, forkRunner
	t.Cleanup(func() { lookupPath, forkRunner = originalLookup, originalRunner })
	lookupPath = func(bin string) (string, error) { return "/fake/" + bin, nil }
	forkRunner = p.run
	p.replies["user"] = []forkTestReply{{status: 200, body: `{"login":"alice"}`}}
	p.replies["repos/upstream/widget"] = []forkTestReply{{status: 200, body: forkTestRepoJSON(10, "upstream/widget", 0, "")}}
	p.replies["repos/alice/widget"] = []forkTestReply{{status: 404, body: `{"message":"Not Found"}`, err: errors.New("HTTP 404")}}
	return p
}

func (p *forkTestProvider) run(_ context.Context, bin, dir string, args ...string) (string, error) {
	p.t.Helper()
	if bin != "gh" || dir != "" {
		p.t.Fatalf("unexpected execution target: %q %q", bin, dir)
	}
	p.calls = append(p.calls, append([]string(nil), args...))
	if len(args) > 1 && args[0] == "repo" && args[1] == "fork" {
		// gh validates Changed("remote"), not its boolean value. Model the
		// native rejection before recording any external write.
		for _, arg := range args[3:] {
			if arg == "--remote" || strings.HasPrefix(arg, "--remote=") {
				return "", errors.New("the `--remote` flag is unsupported when a repository argument is provided")
			}
		}
		want := []string{"repo", "fork", "https://github.com/upstream/widget", "--clone=false"}
		if !reflect.DeepEqual(args, want) {
			p.t.Fatalf("fork args = %q, want %q", args, want)
		}
		p.creates++
		if p.create == nil {
			p.t.Fatal("unexpected remote write")
		}
		return p.create()
	}
	wantPrefix := []string{"api", "--hostname", "github.com", "--method", "GET", "--include"}
	if len(args) != 7 || !reflect.DeepEqual(args[:6], wantPrefix) {
		p.t.Fatalf("unexpected provider argv: %q", args)
	}
	endpoint := args[6]
	replies := p.replies[endpoint]
	if len(replies) == 0 {
		p.t.Fatalf("unexpected endpoint: %s", endpoint)
	}
	reply := replies[0]
	if len(replies) > 1 {
		p.replies[endpoint] = replies[1:]
	}
	if reply.status == 0 {
		return reply.body, reply.err
	}
	return fmt.Sprintf("HTTP/2.0 %d response\r\nContent-Type: application/json\r\n\r\n%s", reply.status, reply.body), reply.err
}

func forkTestRepoJSON(id int64, name string, network int64, parent string) string {
	owner, _, _ := strings.Cut(name, "/")
	repo := map[string]any{
		"id": id, "full_name": name, "html_url": "https://github.com/" + name,
		"clone_url": "https://github.com/" + name + ".git", "ssh_url": "git@github.com:" + name + ".git",
		"default_branch": "main", "owner": map[string]string{"login": owner}, "fork": network != 0,
	}
	if network != 0 {
		repo["parent"] = map[string]any{"id": network, "full_name": parent}
		repo["source"] = map[string]any{"id": network, "full_name": "upstream/widget"}
	}
	raw, _ := json.Marshal(repo)
	return string(raw)
}

func forkTestPlan(t *testing.T) ForkPlan {
	t.Helper()
	plan, err := PlanFork(t.Context(), &gh{}, "upstream/widget")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestGitHubForkPlanReadsWithoutCreating(t *testing.T) {
	p := installForkTestProvider(t)
	plan := forkTestPlan(t)
	if plan.Exists || plan.Owner != "alice" || plan.Source.ID != 10 || plan.Fork.ID != 0 || plan.Fork.FullName != "alice/widget" {
		t.Fatalf("plan = %+v", plan)
	}
	if p.creates != 0 || len(p.calls) != 3 {
		t.Fatalf("planning calls = %q", p.calls)
	}
}

func TestGitHubForkPlanReusesVerifiedNetwork(t *testing.T) {
	p := installForkTestProvider(t)
	p.replies["repos/alice/widget"] = []forkTestReply{{status: 200, body: forkTestRepoJSON(20, "alice/widget", 10, "upstream/widget")}}
	plan := forkTestPlan(t)
	if !plan.Exists || plan.Fork.ID != 20 || plan.Fork.NetworkID != plan.Source.ID {
		t.Fatalf("plan = %+v", plan)
	}
	result, err := ApplyFork(t.Context(), &gh{}, plan)
	if err != nil || !result.Reused || result.Created || result.Unknown || p.creates != 0 {
		t.Fatalf("result = %+v, error = %v, writes = %d", result, err, p.creates)
	}
}

func TestGitHubForkPlanResolvesOwnRenamedForkParent(t *testing.T) {
	p := installForkTestProvider(t)
	p.replies["repos/alice/renamed"] = []forkTestReply{{status: 200, body: forkTestRepoJSON(20, "alice/renamed", 10, "upstream/widget")}}
	plan, err := PlanFork(t.Context(), &gh{}, "git@github.com:alice/renamed.git")
	if err != nil || !plan.Exists || plan.Source.FullName != "upstream/widget" || plan.Fork.FullName != "alice/renamed" {
		t.Fatalf("plan = %+v, error = %v", plan, err)
	}
	result, err := ApplyFork(t.Context(), &gh{}, plan)
	if err != nil || !result.Reused || p.creates != 0 {
		t.Fatalf("apply = %+v, %v", result, err)
	}
}

func TestGitHubForkPlanRejectsUnsafeTargets(t *testing.T) {
	for _, source := range []string{"https://gitlab.com/upstream/widget.git", "https://github.example.com/upstream/widget.git", "../widget", "https://secret@github.com/upstream/widget", "https://github.com/upstream/widget?token=secret", "--help", "owner/.."} {
		t.Run(source, func(t *testing.T) {
			p := installForkTestProvider(t)
			_, err := PlanFork(t.Context(), &gh{}, source)
			if err == nil || len(p.calls) != 0 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe target = %v, calls = %q", err, p.calls)
			}
		})
	}
}

func TestGitHubForkPlanMissingCLIDoesNotProbe(t *testing.T) {
	p := installForkTestProvider(t)
	lookupPath = func(string) (string, error) { return "", errors.New("not found") }
	_, err := PlanFork(t.Context(), &gh{}, "upstream/widget")
	var missing *ErrNoCLI
	if !errors.As(err, &missing) || len(p.calls) != 0 {
		t.Fatalf("error = %v, calls = %q", err, p.calls)
	}
}

func TestGitHubForkPlanDistinguishesAbsentAndOperationalFailures(t *testing.T) {
	tests := []struct {
		name  string
		reply forkTestReply
		auth  bool
	}{
		{"rate limited", forkTestReply{403, `{"message":"rate limit exceeded"}`, errors.New("HTTP 403 rate limit exceeded")}, false},
		{"scope denied", forkTestReply{403, `{"message":"Forbidden"}`, errors.New("try gh auth login with more permissions")}, false},
		{"network", forkTestReply{0, "", errors.New("network timeout")}, false},
		{"not found text only", forkTestReply{0, "", errors.New("wrapper failed: previous request HTTP 404")}, false},
		{"unauthenticated", forkTestReply{401, `{"message":"Bad credentials"}`, errors.New("exit 1")}, true},
		{"bad JSON", forkTestReply{200, `not json`, nil}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := installForkTestProvider(t)
			p.replies["repos/alice/widget"] = []forkTestReply{test.reply}
			_, err := PlanFork(t.Context(), &gh{}, "upstream/widget")
			if err == nil || IsAuth(err) != test.auth || p.creates != 0 {
				t.Fatalf("error = %v, auth = %t, creates = %d", err, IsAuth(err), p.creates)
			}
		})
	}
}

func TestGitHubForkPlanRejectsUnrelatedNameCollision(t *testing.T) {
	for _, candidate := range []string{forkTestRepoJSON(20, "alice/widget", 0, ""), forkTestRepoJSON(20, "alice/widget", 99, "other/widget")} {
		t.Run(candidate, func(t *testing.T) {
			p := installForkTestProvider(t)
			p.replies["repos/alice/widget"] = []forkTestReply{{status: 200, body: candidate}}
			_, err := PlanFork(t.Context(), &gh{}, "upstream/widget")
			if err == nil || !strings.Contains(err.Error(), "not your fork") || p.creates != 0 {
				t.Fatalf("collision = %v, creates = %d", err, p.creates)
			}
		})
	}
}

func TestGitHubForkApplyCreatesAndVerifiesWithoutLocalMutation(t *testing.T) {
	p := installForkTestProvider(t)
	plan := forkTestPlan(t)
	p.create = func() (string, error) {
		p.replies["repos/alice/widget"] = []forkTestReply{{status: 200, body: forkTestRepoJSON(20, "alice/widget", 10, "upstream/widget")}}
		return "untrusted provider output: https://evil.invalid/fork", nil
	}
	result, err := ApplyFork(t.Context(), &gh{}, plan)
	if err != nil || !result.Created || result.Reused || result.Unknown || !result.Plan.Exists || result.Plan.Fork.ID != 20 || p.creates != 1 {
		t.Fatalf("result = %+v, error = %v, writes = %d", result, err, p.creates)
	}
}

func TestGitHubForkApplyUsesCompatibleExplicitRepositoryFlags(t *testing.T) {
	p := installForkTestProvider(t)
	// This is gh 2.101.0's pre-write rejection. Passing false does not make
	// --remote compatible with an explicit repository argument.
	_, err := p.run(t.Context(), "gh", "", "repo", "fork", "https://github.com/upstream/widget", "--clone=false", "--remote=false")
	if err == nil || p.creates != 0 {
		t.Fatalf("native flag conflict = %v, writes = %d", err, p.creates)
	}
	plan := forkTestPlan(t)
	p.create = func() (string, error) {
		p.replies["repos/alice/widget"] = []forkTestReply{{status: 200, body: forkTestRepoJSON(20, "alice/widget", 10, "upstream/widget")}}
		return "", nil
	}
	result, err := ApplyFork(t.Context(), &gh{}, plan)
	if err != nil || !result.Created || p.creates != 1 {
		t.Fatalf("compatible invocation result = %+v, error = %v, writes = %d", result, err, p.creates)
	}
}

func TestGitHubForkApplyReusesForkCreatedAfterPlan(t *testing.T) {
	p := installForkTestProvider(t)
	plan := forkTestPlan(t)
	p.replies["repos/alice/widget"] = []forkTestReply{{status: 200, body: forkTestRepoJSON(20, "alice/widget", 10, "upstream/widget")}}
	result, err := ApplyFork(t.Context(), &gh{}, plan)
	if err != nil || !result.Reused || p.creates != 0 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestGitHubForkApplyWaitsForNewRepositoryVisibility(t *testing.T) {
	p := installForkTestProvider(t)
	plan := forkTestPlan(t)
	p.create = func() (string, error) {
		p.replies["repos/alice/widget"] = []forkTestReply{
			{status: 404, body: `{}`, err: errors.New("HTTP 404")},
			{status: 200, body: forkTestRepoJSON(20, "alice/widget", 10, "upstream/widget")},
		}
		return "", nil
	}
	result, err := ApplyFork(t.Context(), &gh{}, plan)
	if err != nil || !result.Created || p.creates != 1 {
		t.Fatalf("result = %+v, error = %v, writes = %d", result, err, p.creates)
	}
}

func TestGitHubForkApplyStopsForChangedAccountOrRepository(t *testing.T) {
	for _, changed := range []string{"account", "source", "fork removed", "fork replaced"} {
		t.Run(changed, func(t *testing.T) {
			p := installForkTestProvider(t)
			if strings.HasPrefix(changed, "fork") {
				p.replies["repos/alice/widget"] = []forkTestReply{{status: 200, body: forkTestRepoJSON(20, "alice/widget", 10, "upstream/widget")}}
			}
			plan := forkTestPlan(t)
			switch changed {
			case "account":
				p.replies["user"] = []forkTestReply{{status: 200, body: `{"login":"bob"}`}}
			case "source":
				p.replies["repos/upstream/widget"] = []forkTestReply{{status: 200, body: forkTestRepoJSON(11, "upstream/widget", 0, "")}}
			case "fork removed":
				p.replies["repos/alice/widget"] = []forkTestReply{{status: 404, body: `{}`, err: errors.New("HTTP 404")}}
			case "fork replaced":
				p.replies["repos/alice/widget"] = []forkTestReply{{status: 200, body: forkTestRepoJSON(21, "alice/widget", 10, "upstream/widget")}}
			}
			_, err := ApplyFork(t.Context(), &gh{}, plan)
			if !errors.Is(err, ErrForkPlanChanged) || p.creates != 0 {
				t.Fatalf("error = %v, creates = %d", err, p.creates)
			}
		})
	}
}

func TestGitHubForkApplyUnknownWriteReconcilesWithoutRetry(t *testing.T) {
	for _, found := range []bool{false, true} {
		t.Run(fmt.Sprintf("found=%t", found), func(t *testing.T) {
			p := installForkTestProvider(t)
			plan := forkTestPlan(t)
			p.create = func() (string, error) {
				if found {
					p.replies["repos/alice/widget"] = []forkTestReply{{status: 200, body: forkTestRepoJSON(20, "alice/widget", 10, "upstream/widget")}}
				}
				return "", context.DeadlineExceeded
			}
			result, err := ApplyFork(t.Context(), &gh{}, plan)
			if p.creates != 1 || result.Created || result.Reused || result.Unknown == found || (err == nil) != found || result.Plan.Exists != found {
				t.Fatalf("result = %+v, error = %v, writes = %d", result, err, p.creates)
			}
			if !found && !strings.Contains(err.Error(), "inspect https://github.com/alice/widget before retrying") {
				t.Fatalf("missing recovery identity: %v", err)
			}
		})
	}
}

func TestGitHubForkApplyRejectsTamperedPlanBeforeProviderCalls(t *testing.T) {
	p := installForkTestProvider(t)
	plan := forkTestPlan(t)
	p.calls = nil
	plan.Fork.FullName = "bob/widget"
	if _, err := ApplyFork(t.Context(), &gh{}, plan); err == nil || len(p.calls) != 0 {
		t.Fatalf("error = %v, calls = %q", err, p.calls)
	}
}

func TestForkUnsupportedProviderDoesNotInvokeGitHub(t *testing.T) {
	p := installForkTestProvider(t)
	for _, provider := range []Forge{&glab{}, NewAzureDevOps(nil)} {
		_, planErr := PlanFork(t.Context(), provider, "owner/widget")
		_, applyErr := ApplyFork(t.Context(), provider, ForkPlan{})
		var unsupported *ErrUnsupported
		if !errors.As(planErr, &unsupported) || !errors.As(applyErr, &unsupported) {
			t.Fatalf("provider %s: plan %v, apply %v", provider.Kind(), planErr, applyErr)
		}
	}
	if len(p.calls) != 0 {
		t.Fatalf("unsupported calls = %q", p.calls)
	}
}
