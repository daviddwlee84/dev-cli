package forge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestProbeDistinguishesMissingUnauthenticatedAndReady(t *testing.T) {
	originalLookup := lookupPath
	originalProbe := probeRunner
	t.Cleanup(func() {
		lookupPath = originalLookup
		probeRunner = originalProbe
	})

	lookupPath = func(string) (string, error) { return "", errors.New("not found") }
	probeRunner = func(context.Context, string, string, ...string) (string, error) {
		t.Fatal("missing CLI must not run an auth command")
		return "", nil
	}
	missing := Probe(t.Context(), GitHub)
	if missing.Status != ReadinessMissingCLI || missing.Installed || missing.Authenticated || missing.Ready() {
		t.Fatalf("missing readiness = %+v", missing)
	}
	if !strings.Contains(missing.Action, "gh auth login") {
		t.Fatalf("missing action = %q", missing.Action)
	}

	lookupPath = func(bin string) (string, error) { return "/fake/" + bin, nil }
	probeRunner = func(_ context.Context, bin, dir string, args ...string) (string, error) {
		if bin != "gh" || dir != "" {
			t.Fatalf("probe target = %q %q", bin, dir)
		}
		want := []string{"auth", "status", "--hostname", "github.com"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("probe args = %q, want %q", args, want)
		}
		return "", errors.New("not logged in")
	}
	unauthenticated := Probe(t.Context(), GitHub)
	if unauthenticated.Status != ReadinessUnauthenticated || !unauthenticated.Installed || unauthenticated.Authenticated {
		t.Fatalf("unauthenticated readiness = %+v", unauthenticated)
	}
	if !strings.Contains(unauthenticated.Action, "gh auth login") {
		t.Fatalf("unauthenticated action = %q", unauthenticated.Action)
	}

	probeRunner = func(context.Context, string, string, ...string) (string, error) { return "ok", nil }
	ready := Probe(t.Context(), GitHub)
	if ready.Status != ReadinessReady || !ready.Installed || !ready.Authenticated || !ready.Ready() {
		t.Fatalf("ready readiness = %+v", ready)
	}
}

func TestProbeUsesConfiguredGitLabHost(t *testing.T) {
	originalLookup := lookupPath
	originalProbe := probeRunner
	t.Cleanup(func() {
		lookupPath = originalLookup
		probeRunner = originalProbe
	})
	t.Setenv("GITLAB_HOST", "gitlab.example.com")
	lookupPath = func(bin string) (string, error) { return "/fake/" + bin, nil }
	probeRunner = func(_ context.Context, bin, _ string, args ...string) (string, error) {
		want := []string{"auth", "status", "--hostname", "gitlab.example.com"}
		if bin != "glab" || !reflect.DeepEqual(args, want) {
			t.Fatalf("probe = %s %q, want glab %q", bin, args, want)
		}
		return "", nil
	}
	if got := Probe(t.Context(), GitLab); !got.Ready() {
		t.Fatalf("readiness = %+v", got)
	}
}

func TestGitHubPublishRepoLeavesLocalGitToCaller(t *testing.T) {
	originalLookup := lookupPath
	originalPublish := publishRunner
	t.Cleanup(func() {
		lookupPath = originalLookup
		publishRunner = originalPublish
	})
	lookupPath = func(bin string) (string, error) { return "/fake/" + bin, nil }
	var invocation []string
	publishRunner = func(_ context.Context, bin, dir string, args ...string) (string, error) {
		invocation = append([]string{bin, dir}, args...)
		return "created\nhttps://github.example.com/acme/widget", nil
	}
	adapter, _ := For(GitHub)
	result, err := PublishRepo(t.Context(), adapter, "/checkout", RepoRequest{
		Name: "widget", Namespace: "acme", Description: "A widget",
		Visibility: VisibilityInternal, Push: true, RemoteName: "upstream",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gh", "/checkout", "repo", "create", "acme/widget", "--internal", "--description", "A widget"}
	if !reflect.DeepEqual(invocation, want) {
		t.Fatalf("invocation = %q, want %q", invocation, want)
	}
	if result.FullName != "acme/widget" || result.Name != "widget" || result.URL != "https://github.example.com/acme/widget" {
		t.Fatalf("identity = %+v", result)
	}
	if result.RemoteURL != "https://github.example.com/acme/widget.git" || result.SSHURL != "git@github.example.com:acme/widget.git" {
		t.Fatalf("remote URLs = %+v", result)
	}
	if result.RemoteName != "upstream" {
		t.Fatalf("remote name = %q", result.RemoteName)
	}
}

func TestGitLabPublishRepoSupportsNestedNamespace(t *testing.T) {
	originalLookup := lookupPath
	originalPublish := publishRunner
	t.Cleanup(func() {
		lookupPath = originalLookup
		publishRunner = originalPublish
	})
	lookupPath = func(bin string) (string, error) { return "/fake/" + bin, nil }
	var invocation []string
	publishRunner = func(_ context.Context, bin, dir string, args ...string) (string, error) {
		invocation = append([]string{bin, dir}, args...)
		return "Created project at https://gitlab.example.com/acme/platform/widget", nil
	}
	adapter, _ := For(GitLab)
	result, err := PublishRepo(t.Context(), adapter, "/checkout", RepoRequest{
		FullName: "acme/platform/widget", Visibility: VisibilityPrivate,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"glab", "/checkout", "repo", "create", "--name", "widget", "--skipGitInit", "--private", "--group", "acme/platform"}
	if !reflect.DeepEqual(invocation, want) {
		t.Fatalf("invocation = %q, want %q", invocation, want)
	}
	if result.FullName != "acme/platform/widget" || result.RemoteURL != "https://gitlab.example.com/acme/platform/widget.git" {
		t.Fatalf("result = %+v", result)
	}
}

func TestPublishFailureDoesNotInventRemoteSuccess(t *testing.T) {
	originalLookup := lookupPath
	originalPublish := publishRunner
	t.Cleanup(func() {
		lookupPath = originalLookup
		publishRunner = originalPublish
	})
	lookupPath = func(bin string) (string, error) { return "/fake/" + bin, nil }
	publishRunner = func(context.Context, string, string, ...string) (string, error) {
		return "", errors.New("provider rejected the request")
	}
	adapter, _ := For(GitHub)
	result, err := PublishRepo(t.Context(), adapter, t.TempDir(), RepoRequest{
		FullName: "acme/widget", Visibility: VisibilityPrivate,
	})
	if err == nil {
		t.Fatal("publish should report the provider error")
	}
	if result.RemoteURL != "" || result.URL != "" {
		t.Fatalf("failed publish invented remote URLs: %+v", result)
	}
}

func TestRepoRequestCompatibilityAndValidation(t *testing.T) {
	legacyPrivate, err := resolveRepoRequest(RepoRequest{Name: "widget", Private: true})
	if err != nil {
		t.Fatal(err)
	}
	if legacyPrivate.fullName != "widget" || legacyPrivate.visibility != VisibilityPrivate || legacyPrivate.remoteName != "origin" {
		t.Fatalf("legacy request = %+v", legacyPrivate)
	}
	explicit, err := resolveRepoRequest(RepoRequest{
		Name: "ignored", Namespace: "ignored", FullName: "group/sub/widget",
		Private: true, Visibility: VisibilityPublic, RemoteName: "upstream",
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.name != "widget" || explicit.namespace != "group/sub" || explicit.visibility != VisibilityPublic || explicit.remoteName != "upstream" {
		t.Fatalf("explicit request = %+v", explicit)
	}
	for _, req := range []RepoRequest{
		{},
		{Name: "https://github.com/acme/widget"},
		{Name: "widget", Visibility: "secret"},
		{FullName: "group/../widget"},
	} {
		if _, err := resolveRepoRequest(req); err == nil {
			t.Errorf("request %+v should fail", req)
		}
	}
}

func TestPublishRepoRejectsUnsupportedAdapter(t *testing.T) {
	_, err := PublishRepo(t.Context(), NewAzureDevOps(nil), t.TempDir(), RepoRequest{Name: "widget"})
	var unsupported *ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %T %v", err, err)
	}
}
