package forge_test

import (
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/forge"
)

func TestFromURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want forge.Kind
	}{
		{"https://github.com/owner/repo.git", forge.GitHub},
		{"git@github.com:owner/repo.git", forge.GitHub},
		{"https://gitlab.com/group/repo.git", forge.GitLab},
		{"git@gitlab.example.com:group/repo.git", forge.GitLab},
		{"https://dev.azure.com/acme/Platform/_git/api", forge.AzureDevOps},
		{"git@ssh.dev.azure.com:v3/acme/Platform/api", forge.AzureDevOps},
		{"https://acme.visualstudio.com/DefaultCollection/Platform/_git/api", forge.AzureDevOps},
		{"git@evildev.azure.com:v3/acme/Platform/api", forge.Unknown},
		{"https://git.sr.ht/~user/repo", forge.Unknown},
		{"", forge.Unknown},
	} {
		if got := forge.FromURL(tc.url); got != tc.want {
			t.Errorf("FromURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestForReturnsAdapters(t *testing.T) {
	gh, err := forge.For(forge.GitHub)
	if err != nil || gh.Bin() != "gh" {
		t.Errorf("GitHub adapter: %v %v", gh, err)
	}
	gl, err := forge.For(forge.GitLab)
	if err != nil || gl.Bin() != "glab" {
		t.Errorf("GitLab adapter: %v %v", gl, err)
	}
	az, err := forge.For(forge.AzureDevOps)
	if err != nil || az.Bin() != "az" {
		t.Errorf("Azure DevOps adapter: %v %v", az, err)
	}
	if _, err := forge.For(forge.Unknown); err == nil {
		t.Error("an unknown host has no adapter, and callers must fall back to plain git")
	}
}

func TestCloneURL(t *testing.T) {
	gh, _ := forge.For(forge.GitHub)
	if got := gh.CloneURL("owner/repo"); got != "https://github.com/owner/repo.git" {
		t.Errorf("shorthand: %q", got)
	}
	// A full URL must pass through untouched, or an enterprise host would be
	// rewritten to github.com.
	full := "https://github.example.com/owner/repo.git"
	if got := gh.CloneURL(full); got != full {
		t.Errorf("full URL should pass through, got %q", got)
	}
	if got := gh.CloneURL("git@github.com:owner/repo.git"); got != "git@github.com:owner/repo.git" {
		t.Errorf("ssh URL should pass through, got %q", got)
	}

	gl, _ := forge.For(forge.GitLab)
	if got := gl.CloneURL("group/repo"); got != "https://gitlab.com/group/repo.git" {
		t.Errorf("gitlab shorthand: %q", got)
	}
}

// Every entry point must fail with a clear, actionable error rather than
// panicking when the CLI is absent — the whole point of treating forges as
// optional.
func TestMissingCLIErrorsClearly(t *testing.T) {
	for _, k := range []forge.Kind{forge.GitHub, forge.GitLab} {
		f, _ := forge.For(k)
		if f.Available() {
			continue // installed on this machine; nothing to assert
		}
		if _, err := f.CreatePR(t.Context(), t.TempDir(), forge.PRRequest{Base: "main", Head: "x"}); err == nil {
			t.Errorf("%s CreatePR should error when the CLI is missing", k)
		} else if _, ok := err.(*forge.ErrNoCLI); !ok {
			t.Errorf("%s should report ErrNoCLI, got %T", k, err)
		}
	}
}

func TestIdentityFromURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		kind forge.Kind
		name string
	}{
		{"https://github.com/owner/repo.git", forge.GitHub, "owner/repo"},
		{"git@github.com:owner/repo.git", forge.GitHub, "owner/repo"},
		{"ssh://git@gitlab.com/group/sub/repo.git", forge.GitLab, "group/sub/repo"},
		{"https://gitlab.example.com/group/repo", forge.GitLab, "group/repo"},
		{"https://acme@dev.azure.com/acme/Platform%20Tools/_git/api.git", forge.AzureDevOps, "acme/Platform Tools/api"},
		{"git@ssh.dev.azure.com:v3/acme/Platform/api", forge.AzureDevOps, "acme/Platform/api"},
		{"ssh://git@ssh.dev.azure.com/v3/acme/Platform/api", forge.AzureDevOps, "acme/Platform/api"},
		{"https://acme.visualstudio.com/DefaultCollection/Platform/_git/api", forge.AzureDevOps, "acme/Platform/api"},
		{"acme@vs-ssh.visualstudio.com:Platform/_ssh/api", forge.AzureDevOps, "acme/Platform/api"},
		{"", forge.Unknown, ""},
	} {
		kind, name := forge.IdentityFromURL(tc.url)
		if kind != tc.kind || name != tc.name {
			t.Errorf("IdentityFromURL(%q) = %q, %q; want %q, %q", tc.url, kind, name, tc.kind, tc.name)
		}
	}
}
