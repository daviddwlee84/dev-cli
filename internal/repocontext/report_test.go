package repocontext_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/assessment"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/repocontext"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func cleanContext() inventory.RepoContext {
	return inventory.RepoContext{
		Repo: repo.Repo{
			Name: "demo", Path: "/src/demo", RealPath: "/src/demo",
			CommonDir: "/src/demo/.git", HasGit: true,
		},
		Runtime: "none",
		Checkouts: []inventory.RepoCheckout{{
			Worktree: gitx.Worktree{Path: "/src/demo", Branch: "main", Main: true},
			Exists:   true, Ownership: inventory.CheckoutCanonical,
			Status: gitx.Status{Branch: "main", Upstream: "origin/main"},
		}},
	}
}

func TestReportJSONContractRedactsRemotesAndMatchesFleetByIdentity(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	remote := "https://alice:sekrit@github.com/acme/demo.git?token=hush#private"
	topology := gitx.RecoveryTopology{Remotes: []gitx.RemoteInfo{
		{Name: "origin", FetchURLs: []string{remote}, PushURLs: []string{remote}},
		{Name: "upstream", FetchURLs: []string{"https://gitlab.com/acme/upstream.git"}},
	}}
	forgeRecords := []forge.RemoteRepo{
		{Forge: forge.GitHub, FullName: "acme/demo", URL: "https://github.com/acme/demo", CloneURL: "https://github.com/acme/demo.git"},
		{Forge: forge.GitHub, FullName: "acme/demo", URL: "https://github.com/acme/demo/", CloneURL: "git@github.com:acme/demo.git"},
	}
	cachedAt := now.Add(-2 * time.Hour)
	report, err := repocontext.Build(repocontext.BuildInput{
		GeneratedAt: now, Context: cleanContext(), SelectedCheckout: 0,
		Topology: topology, Hostname: "laptop",
		Runtimes: []repocontext.RuntimeInput{{Backend: "none", Available: true}},
		Forge: repocontext.ForgeInput{
			Authority: assessment.AuthorityCache, Freshness: assessment.FreshnessStale,
			Completeness: assessment.CompletenessComplete, ObservedAt: cachedAt,
			Records: forgeRecords,
		},
		Fleet: repocontext.FleetInput{
			Configured: true, ConfiguredHostNames: []string{"lab", "desk"}, CacheTTL: 15 * time.Minute,
			Results: []fleet.HostResult{
				{
					Name: "lab", State: fleet.HostStale, FromCache: true, CachedAt: &cachedAt,
					Snapshot: &fleet.Snapshot{SchemaVersion: 1, Host: "lab", GeneratedAt: cachedAt, Repositories: []fleet.RepoSnapshot{
						{Name: "not-demo", Display: "other/not-demo", Path: "/work/not-demo", Branch: "main", RemoteIdentities: []string{"github.com/acme/demo"}},
						{Name: "demo", Display: "demo", Path: "/work/demo", Branch: "main", RemoteIdentities: []string{"gitlab.com/other/demo"}},
					}},
				},
				{
					Name: "desk", State: fleet.HostOK,
					Snapshot: &fleet.Snapshot{SchemaVersion: 1, Host: "desk", GeneratedAt: now, Repositories: []fleet.RepoSnapshot{
						{Name: "demo", Display: "demo", Path: "/desk/demo", Branch: "main", RemoteIdentities: []string{"gitlab.com/other/demo"}},
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.GeneratedAt != now || report.SelectedCheckout == nil || report.SelectedCheckout.Path != "/src/demo" {
		t.Fatalf("report envelope = %+v", report)
	}
	if err := report.Assessment.Validate(); err != nil {
		t.Fatalf("assessment is not a valid sealed report: %v", err)
	}
	if len(report.Remotes) != 2 || len(report.Remotes[0].Fetch) != 1 {
		t.Fatalf("remotes = %+v", report.Remotes)
	}
	if len(report.Remotes[1].Roles) != 1 || report.Remotes[1].Roles[0] != repocontext.RemoteRoleUpstream {
		t.Errorf("upstream role = %v", report.Remotes[1].Roles)
	}
	observedRemote := report.Remotes[0]
	if len(observedRemote.Roles) != 2 || observedRemote.Roles[0] != repocontext.RemoteRoleCurrentUpstream || observedRemote.Roles[1] != repocontext.RemoteRoleOrigin {
		t.Errorf("origin roles = %v", observedRemote.Roles)
	}
	endpoint := observedRemote.Fetch[0]
	if endpoint.Identity == nil || *endpoint.Identity != "github.com/acme/demo" {
		t.Errorf("sanitized endpoint identity = %v", endpoint.Identity)
	}
	if endpoint.ForgeMatch != "ambiguous" || endpoint.Error == nil {
		t.Errorf("ambiguous forge match was not retained: %+v", endpoint)
	}
	if endpoint.WebURL == nil || endpoint.WebURL.URL != "https://github.com/acme/demo" || endpoint.WebURL.Source != forge.WebURLSourceGitRemote {
		t.Errorf("safe fallback web URL = %+v", endpoint.WebURL)
	}

	if report.Fleet.Coverage != repocontext.FleetCoverageConfiguredHostsOnly || report.Fleet.ConfiguredHosts == nil || *report.Fleet.ConfiguredHosts != 2 {
		t.Fatalf("fleet coverage = %+v", report.Fleet)
	}
	if got := report.Fleet.Hosts[0]; got.Match != repocontext.FleetMatchExact || len(got.Repositories) != 1 || got.Repositories[0].Name != "not-demo" {
		t.Errorf("identity match must ignore fuzzy names: %+v", got)
	}
	if got := report.Fleet.Hosts[1]; got.Match != repocontext.FleetMatchNotFound || len(got.Repositories) != 0 {
		t.Errorf("same name with different identity must not match: %+v", got)
	}

	forgeSource := source(t, report, "forge.inventory")
	labSource := source(t, report, "fleet.host.0")
	deskSource := source(t, report, "fleet.host.1")
	if forgeSource.Authority != assessment.AuthorityCache || forgeSource.Freshness != assessment.FreshnessStale || forgeSource.AgeSeconds != 7200 {
		t.Errorf("forge cache provenance = %+v", forgeSource)
	}
	if labSource.Authority != assessment.AuthorityCache || labSource.Freshness != assessment.FreshnessStale {
		t.Errorf("fleet cache provenance = %+v", labSource)
	}
	if deskSource.Authority != assessment.AuthorityRemoteLive || deskSource.Freshness != assessment.FreshnessFresh {
		t.Errorf("live fleet provenance = %+v", deskSource)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	for _, secret := range []string{"alice", "sekrit", "token=hush", "#private"} {
		if strings.Contains(output, secret) {
			t.Errorf("JSON leaked %q: %s", secret, output)
		}
	}
	var contract map[string]any
	if err := json.Unmarshal(encoded, &contract); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "generated_at", "repository", "selected_checkout", "sources", "capabilities", "local", "remotes", "forge", "fleet", "assessment", "errors"} {
		if _, ok := contract[key]; !ok {
			t.Errorf("JSON contract missing %q", key)
		}
	}
}

func TestReportRejectsCredentialBearingSCPLikeIdentity(t *testing.T) {
	credential := "credential-must-not-appear"
	remote := "oauth2:" + credential + "@example.test:acme/demo.git"
	report, err := repocontext.Build(repocontext.BuildInput{
		GeneratedAt: time.Date(2026, 8, 31, 12, 15, 0, 0, time.UTC),
		Context:     cleanContext(),
		Topology: gitx.RecoveryTopology{Remotes: []gitx.RemoteInfo{{
			Name: "origin", FetchURLs: []string{remote},
		}}},
		Fleet: repocontext.FleetInput{Configured: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := report.Remotes[0].Fetch[0]
	if endpoint.Identity != nil {
		t.Fatalf("credential-bearing SCP-like remote gained public identity %q", *endpoint.Identity)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), credential) {
		t.Fatalf("public report leaked credential-bearing remote: %s", encoded)
	}
}

func TestReportRedactsCredentialBearingDiagnosticToken(t *testing.T) {
	secret := "diagnostic-token-must-not-appear"
	report, err := repocontext.Build(repocontext.BuildInput{
		GeneratedAt: time.Date(2026, 8, 31, 12, 20, 0, 0, time.UTC),
		Context:     cleanContext(), SelectedCheckout: 0,
		TopologyErr: errors.New("fetch user:" + secret + "@example.test:acme/demo.git failed"),
		Fleet:       repocontext.FleetInput{Configured: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("diagnostic leaked credential token: %s", encoded)
	}
}

func TestRemoteUsesExactForgeWebURLMetadata(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 30, 0, 0, time.UTC)
	report, err := repocontext.Build(repocontext.BuildInput{
		GeneratedAt: now, Context: cleanContext(), SelectedCheckout: 0,
		Topology: gitx.RecoveryTopology{Remotes: []gitx.RemoteInfo{{
			Name: "origin", FetchURLs: []string{"git@github.com:acme/demo.git"},
		}}},
		Forge: repocontext.ForgeInput{
			Authority: assessment.AuthorityRemoteLive, Freshness: assessment.FreshnessFresh,
			Completeness: assessment.CompletenessComplete, ObservedAt: now,
			Records: []forge.RemoteRepo{{
				Forge: forge.GitHub, FullName: "acme/demo", URL: "https://github.com/acme/demo",
				CloneURL: "https://github.com/acme/demo.git", SSHURL: "git@github.com:acme/demo.git",
			}},
		},
		Fleet: repocontext.FleetInput{Configured: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := report.Remotes[0].Fetch[0]
	if endpoint.ForgeMatch != "exact" || endpoint.WebURL == nil ||
		endpoint.WebURL.URL != "https://github.com/acme/demo" ||
		endpoint.WebURL.Provider != forge.GitHub || endpoint.WebURL.Source != forge.WebURLSourceForgeRecord ||
		endpoint.WebURL.Confidence != forge.WebURLConfidenceExact {
		t.Fatalf("exact forge metadata = %+v", endpoint)
	}
}

func TestReportPreservesRuntimeAndStatusErrorsAsUnknown(t *testing.T) {
	now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	context := cleanContext()
	context.Checkouts[0].Status = gitx.Status{}
	context.Checkouts[0].StatusErr = errors.New("status failed")
	context.Runtime = "herdr"
	context.RuntimeErr = errors.New("runtime failed")
	report, err := repocontext.Build(repocontext.BuildInput{
		GeneratedAt: now, Context: context, SelectedCheckout: 0,
		Runtimes: []repocontext.RuntimeInput{{Backend: "herdr", Available: true, Err: context.RuntimeErr}},
		Fleet:    repocontext.FleetInput{Configured: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkout := report.Local.Checkouts[0]
	if checkout.Status != nil || checkout.StatusError == nil || *checkout.StatusError != "status failed" || checkout.State != "error" {
		t.Fatalf("failed status collapsed to a value: %+v", checkout)
	}
	if len(report.Local.Runtimes) != 1 || report.Local.Runtimes[0].Error == nil || len(report.Local.Runtimes[0].Sessions) != 0 {
		t.Fatalf("failed runtime collapsed to closed: %+v", report.Local.Runtimes)
	}
	if gate(t, report, "checkout-readiness").Outcome != assessment.OutcomeIndeterminate {
		t.Errorf("checkout error must be indeterminate: %+v", report.Assessment.Gates)
	}
	if gate(t, report, "whole-clone-eviction").Outcome != assessment.OutcomeIndeterminate {
		t.Errorf("context must never claim whole-clone eviction eligibility: %+v", report.Assessment.Gates)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"status":null`) || !strings.Contains(string(encoded), `"status_error":"status failed"`) {
		t.Errorf("unknown/error JSON shape changed: %s", encoded)
	}
}

func TestLocalReadinessKeepsScopesIndependent(t *testing.T) {
	context := cleanContext()
	context.Checkouts = append(context.Checkouts, inventory.RepoCheckout{
		Worktree: gitx.Worktree{Path: "/wt/demo", Branch: "feat/demo"},
		Exists:   true, Ownership: inventory.CheckoutDev,
		Status: gitx.Status{Branch: "feat/demo", Upstream: "origin/feat/demo"},
		Tasks: []*task.Task{{
			ID: "demo__feat-demo", Name: "demo", Repo: "demo", RepoPath: "/src/demo",
			WorktreePath: "/wt/demo", Branch: "feat/demo", State: task.Warm, Owner: "laptop",
		}},
	})
	readiness := repocontext.AssessLocal(context, 1, "laptop")
	if readiness.Checkout.Outcome != assessment.OutcomeEligible || readiness.Task.Outcome != assessment.OutcomeEligible || readiness.Worktree.Outcome != assessment.OutcomeEligible {
		t.Fatalf("clean selected worktree readiness = %+v", readiness)
	}
	context.Checkouts[1].Status.Changed = 1
	readiness = repocontext.AssessLocal(context, 1, "other-host")
	if readiness.Checkout.Outcome != assessment.OutcomeBlocked || readiness.Task.Outcome != assessment.OutcomeBlocked || readiness.Worktree.Outcome != assessment.OutcomeBlocked {
		t.Fatalf("known independent blockers = %+v", readiness)
	}
	context.Checkouts[1].StatusErr = errors.New("status unknown")
	context.Checkouts[1].Status = gitx.Status{}
	context.TaskErr = errors.New("tasks unknown")
	context.WorktreeErr = errors.New("worktrees unknown")
	readiness = repocontext.AssessLocal(context, 1, "laptop")
	if readiness.Checkout.Outcome != assessment.OutcomeIndeterminate || readiness.Task.Outcome != assessment.OutcomeIndeterminate || readiness.Worktree.Outcome != assessment.OutcomeIndeterminate {
		t.Fatalf("collection errors must remain indeterminate = %+v", readiness)
	}
}

func TestLocalReadinessIncludesUnattachedRepositoryTasks(t *testing.T) {
	context := cleanContext()
	context.OtherTasks = []*task.Task{{
		ID: "demo__feat-cold", Name: "cold task", Repo: "demo", RepoPath: "/src/demo",
		Branch: "feat/cold", Base: "main", Mode: task.ModeWorktree, State: task.Cold,
		Owner: "other-host",
	}}
	readiness := repocontext.AssessLocal(context, 0, "laptop")
	if readiness.Task.Outcome != assessment.OutcomeBlocked || !hasReasonCode(readiness.Task.Reasons, "task-owned-elsewhere") {
		t.Fatalf("unattached task ownership was omitted: %+v", readiness.Task)
	}

	context.OtherTasks[0].Owner = "laptop"
	context.OtherTasks[0].State = task.Hot
	readiness = repocontext.AssessLocal(context, 0, "laptop")
	if readiness.Task.Outcome != assessment.OutcomeBlocked || !hasReasonCode(readiness.Task.Reasons, "hot-task-runtime-missing") {
		t.Fatalf("unattached HOT task runtime was omitted: %+v", readiness.Task)
	}
}

func hasReasonCode(reasons []assessment.Reason, code string) bool {
	for _, reason := range reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func TestFleetAmbiguityRetainsEveryExactCandidate(t *testing.T) {
	now := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	report, err := repocontext.Build(repocontext.BuildInput{
		GeneratedAt: now, Context: cleanContext(), SelectedCheckout: 0,
		Topology: gitx.RecoveryTopology{Remotes: []gitx.RemoteInfo{{Name: "origin", FetchURLs: []string{"git@github.com:acme/demo.git"}}}},
		Fleet: repocontext.FleetInput{
			Configured: true, ConfiguredHostNames: []string{"lab"},
			Results: []fleet.HostResult{{Name: "lab", State: fleet.HostOK, Snapshot: &fleet.Snapshot{
				SchemaVersion: 1, GeneratedAt: now, Repositories: []fleet.RepoSnapshot{
					{Name: "one", Display: "one", Path: "/one", RemoteIdentities: []string{"github.com/acme/demo"}},
					{Name: "two", Display: "two", Path: "/two", RemoteIdentities: []string{"git@github.com:acme/demo.git"}},
				},
			}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := report.Fleet.Hosts[0]
	if host.Match != repocontext.FleetMatchAmbiguous || len(host.Repositories) != 2 || host.Error == nil {
		t.Fatalf("fleet ambiguity = %+v", host)
	}
	found := false
	for _, reportError := range report.Errors {
		if reportError.Code == "fleet-match-ambiguous" {
			found = true
		}
	}
	if !found {
		t.Errorf("fleet ambiguity missing from errors: %+v", report.Errors)
	}
}

func source(t *testing.T, report repocontext.Report, id string) repocontext.Source {
	t.Helper()
	for _, candidate := range report.Sources {
		if candidate.ID == id {
			return candidate
		}
	}
	t.Fatalf("source %q not found in %+v", id, report.Sources)
	return repocontext.Source{}
}

func gate(t *testing.T, report repocontext.Report, code string) assessment.Gate {
	t.Helper()
	for _, candidate := range report.Assessment.Gates {
		if candidate.Code == code {
			return candidate
		}
	}
	t.Fatalf("gate %q not found in %+v", code, report.Assessment.Gates)
	return assessment.Gate{}
}
