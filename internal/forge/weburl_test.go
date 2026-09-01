package forge_test

import (
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/forge"
)

func TestDeriveWebURLPrefersAndSanitizesExactForgeRecord(t *testing.T) {
	exact := forge.RemoteRepo{
		Forge: forge.GitHub, FullName: "owner/repo",
		URL: "https://alice:password@github.enterprise.test/owner/repo.git?access_token=query-secret#fragment-secret",
	}
	got, ok := forge.DeriveWebURL(forge.WebURLRequest{
		Remote: "git@work-alias:owner/repo.git", Exact: &exact,
	})
	if !ok {
		t.Fatal("exact forge record was unavailable")
	}
	if got.URL != "https://github.enterprise.test/owner/repo" || got.Provider != forge.GitHub ||
		got.Source != forge.WebURLSourceForgeRecord || got.Confidence != forge.WebURLConfidenceExact {
		t.Fatalf("derived URL = %+v", got)
	}
	if strings.Contains(got.URL, "alice") || strings.Contains(got.URL, "secret") || strings.ContainsAny(got.URL, "?#") {
		t.Fatalf("exact URL was not sanitized: %q", got.URL)
	}
}

func TestDeriveWebURLFromGitHubAndGitLabRemotes(t *testing.T) {
	t.Setenv("GH_HOST", "")
	t.Setenv("GITLAB_HOST", "")
	for _, test := range []struct {
		name     string
		request  forge.WebURLRequest
		url      string
		provider forge.Kind
	}{
		{
			name:    "GitHub HTTPS strips credentials",
			request: forge.WebURLRequest{Remote: "https://token:password@github.com/owner/repo.git?token=query#fragment"},
			url:     "https://github.com/owner/repo", provider: forge.GitHub,
		},
		{
			name:    "GitHub public SCP",
			request: forge.WebURLRequest{Remote: "git@github.com:owner/repo.git"},
			url:     "https://github.com/owner/repo", provider: forge.GitHub,
		},
		{
			name: "configured GitHub enterprise SSH",
			request: forge.WebURLRequest{
				Remote: "ssh://git@github.enterprise.test:22/owner/repo.git", GitHubHosts: []string{"github.enterprise.test"},
			},
			url: "https://github.enterprise.test/owner/repo", provider: forge.GitHub,
		},
		{
			name:    "GitLab nested groups",
			request: forge.WebURLRequest{Remote: "git@gitlab.com:group/subgroup/repo.git"},
			url:     "https://gitlab.com/group/subgroup/repo", provider: forge.GitLab,
		},
		{
			name:    "literal GitLab enterprise HTTPS",
			request: forge.WebURLRequest{Remote: "https://gitlab.enterprise.test/group/sub/repo.git"},
			url:     "https://gitlab.enterprise.test/group/sub/repo", provider: forge.GitLab,
		},
		{
			name: "configured non-branded GitLab SSH host",
			request: forge.WebURLRequest{
				Remote: "git@source.corp.test:group/repo.git", GitLabHosts: []string{"source.corp.test"},
			},
			url: "https://source.corp.test/group/repo", provider: forge.GitLab,
		},
		{
			name:    "configured GitLab HTTPS host",
			request: forge.WebURLRequest{Remote: "https://code.enterprise.test/group/sub/repo.git", GitLabHosts: []string{"code.enterprise.test"}},
			url:     "https://code.enterprise.test/group/sub/repo", provider: forge.GitLab,
		},
		{
			name:    "configured GitHub URL host",
			request: forge.WebURLRequest{Remote: "https://source.enterprise.test/owner/repo", GitHubHosts: []string{"https://source.enterprise.test/"}},
			url:     "https://source.enterprise.test/owner/repo", provider: forge.GitHub,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := forge.DeriveWebURL(test.request)
			if !ok {
				t.Fatal("URL was unavailable")
			}
			if got.URL != test.url || got.Provider != test.provider || got.Source != forge.WebURLSourceGitRemote ||
				got.Confidence != forge.WebURLConfidenceConservative {
				t.Fatalf("derived URL = %+v", got)
			}
		})
	}
}

func TestDeriveWebURLFromStrictAzureForms(t *testing.T) {
	for _, test := range []struct {
		remote string
		url    string
	}{
		{"https://acme@dev.azure.com/acme/Platform%20Tools/_git/api.git", "https://dev.azure.com/acme/Platform%20Tools/_git/api"},
		{"git@ssh.dev.azure.com:v3/acme/Platform/api.git", "https://dev.azure.com/acme/Platform/_git/api"},
		{"ssh://git@ssh.dev.azure.com:22/v3/acme/Platform/api", "https://dev.azure.com/acme/Platform/_git/api"},
		{"https://acme.visualstudio.com/DefaultCollection/Platform/_git/api", "https://acme.visualstudio.com/Platform/_git/api"},
		{"acme@vs-ssh.visualstudio.com:Platform/_ssh/api", "https://acme.visualstudio.com/Platform/_git/api"},
	} {
		got, ok := forge.DeriveWebURL(forge.WebURLRequest{Remote: test.remote})
		if !ok {
			t.Errorf("Azure remote %q was unavailable", test.remote)
			continue
		}
		if got.URL != test.url || got.Provider != forge.AzureDevOps || got.Source != forge.WebURLSourceAzureRemote ||
			got.Confidence != forge.WebURLConfidenceStrict {
			t.Errorf("Azure remote %q = %+v", test.remote, got)
		}
	}
}

func TestDeriveWebURLRejectsAliasesLocalAndHostileRemotes(t *testing.T) {
	t.Setenv("GH_HOST", "")
	t.Setenv("GITLAB_HOST", "")
	invalidUTF8 := "https://github.com/owner/" + string([]byte{0xff})
	for _, remote := range []string{
		"git@work:owner/repo.git",
		"git@github.enterprise.test:owner/repo.git",
		"work:owner/repo.git",
		"/tmp/repo",
		"../repo",
		"file:///tmp/repo",
		"http://github.com/owner/repo",
		"git://github.com/owner/repo",
		"https://github.com:8443/owner/repo",
		"ssh://git@github.com:2222/owner/repo",
		"ssh://git:password@github.com/owner/repo",
		"https://evilgithub.com/owner/repo",
		"https://github.com/group/sub/repo",
		"https://gitlab.com/repo",
		"https://github.com/owner/repo%2Fother",
		"https://github.com/owner/repo%5cother",
		"https://github.com/owner/repo%252fother",
		"https://github.com/owner/repo%25252fother",
		"https://github.com/owner/repo\x1b[31m",
		"https://github.com/owner/" + string(rune(0x202e)) + "repo",
		"https://user:password@dev.azure.com/acme/Platform/_git/api",
		"https://dev.azure.com/acme/Platform/_git/api?token=secret",
		"https://dev.azure.com/acme/Platform/_git/api%5cother",
		"https://dev.azure.com/acme/Platform/_git/api%25252fother",
		"git@ssh.dev.azure.com:v3/acme/Platform/api?token=secret",
		"acme:password@vs-ssh.visualstudio.com:Platform/_ssh/api",
		"https://acme.visualstudio.com/Unexpected/Prefix/Platform/_git/api",
		invalidUTF8,
	} {
		request := forge.WebURLRequest{Remote: remote, GitHubHosts: []string{"work"}}
		if got, ok := forge.DeriveWebURL(request); ok {
			t.Errorf("DeriveWebURL(%q) unexpectedly returned %+v", remote, got)
		}
	}
}

func TestDeriveWebURLRejectsAmbiguousAndMismatchedEvidence(t *testing.T) {
	t.Setenv("GH_HOST", "")
	t.Setenv("GITLAB_HOST", "")
	if got, ok := forge.DeriveWebURL(forge.WebURLRequest{
		Remote:      "https://code.example.test/owner/repo",
		GitHubHosts: []string{"code.example.test"}, GitLabHosts: []string{"code.example.test"},
	}); ok {
		t.Fatalf("ambiguous configured host returned %+v", got)
	}

	exact := forge.RemoteRepo{Forge: forge.GitLab, FullName: "group/expected", URL: "https://gitlab.example.test/group/other"}
	if got, ok := forge.DeriveWebURL(forge.WebURLRequest{Exact: &exact}); ok {
		t.Fatalf("mismatched exact record returned %+v", got)
	}
}

func TestDeriveWebURLUsesConfiguredEnvironmentWebHost(t *testing.T) {
	t.Setenv("GH_HOST", "source.corp.test")
	t.Setenv("GITLAB_HOST", "")
	for _, remote := range []string{
		"https://source.corp.test/owner/repo.git",
		"git@source.corp.test:owner/repo.git",
	} {
		got, ok := forge.DeriveWebURL(forge.WebURLRequest{Remote: remote})
		if !ok || got.Provider != forge.GitHub || got.URL != "https://source.corp.test/owner/repo" {
			t.Fatalf("environment host %q result = %+v, %v", remote, got, ok)
		}
	}
}
