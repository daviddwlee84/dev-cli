package forge

import (
	"strings"
	"testing"
	"time"
)

func TestParseGitHubRepos(t *testing.T) {
	fixture := `[{
		"default_branch":"main",
		"description":"A useful repo",
		"archived":false,
		"fork":true,
		"name":"project",
		"full_name":"owner/project",
		"pushed_at":"2026-08-27T07:45:44Z",
		"ssh_url":"git@github.com:owner/project.git",
		"html_url":"https://github.com/owner/project",
		"clone_url":"https://github.com/owner/project.git",
		"visibility":"private"
	}]`
	got, err := parseGitHubRepos(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	r := got[0]
	if r.Forge != GitHub || r.Name != "project" || r.FullName != "owner/project" {
		t.Errorf("identity: %+v", r)
	}
	if r.CloneURL != "https://github.com/owner/project.git" || r.SSHURL == "" {
		t.Errorf("clone URLs: %+v", r)
	}
	if !r.Fork || r.Archived || r.DefaultBranch != "main" || r.Visibility != "private" {
		t.Errorf("metadata: %+v", r)
	}
	if r.UpdatedAt.IsZero() {
		t.Error("pushedAt should parse")
	}
}

func TestParseGitLabRepos(t *testing.T) {
	fixture := `[{
		"name":"project",
		"path_with_namespace":"group/sub/project",
		"description":"GitLab repo",
		"default_branch":"trunk",
		"visibility":"internal",
		"ssh_url_to_repo":"git@gitlab.com:group/sub/project.git",
		"http_url_to_repo":"https://gitlab.com/group/sub/project.git",
		"web_url":"https://gitlab.com/group/sub/project",
		"archived":true,
		"forked_from_project":{"id":1},
		"last_activity_at":"2026-08-27T07:45:44.123Z"
	}]`
	got, err := parseGitLabRepos(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	r := got[0]
	if r.Forge != GitLab || r.Name != "project" || r.FullName != "group/sub/project" {
		t.Errorf("identity: %+v", r)
	}
	if r.CloneURL == "" || r.URL == "" || !r.Archived || !r.Fork {
		t.Errorf("metadata: %+v", r)
	}
	if r.DefaultBranch != "trunk" || r.Visibility != "internal" || r.UpdatedAt.IsZero() {
		t.Errorf("metadata: %+v", r)
	}
}

func TestParseAzureDevOpsRepos(t *testing.T) {
	fixture := `[{
		"name":"api",
		"defaultBranch":"refs/heads/main",
		"isFork":true,
		"remoteUrl":"https://acme@dev.azure.com/acme/Platform%20Tools/_git/api",
		"sshUrl":"git@ssh.dev.azure.com:v3/acme/Platform Tools/api",
		"project":{"name":"Platform Tools","visibility":"private"}
	}]`
	got, err := parseAzureDevOpsRepos(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	r := got[0]
	if r.Forge != AzureDevOps || r.FullName != "acme/Platform Tools/api" || r.Name != "api" {
		t.Errorf("identity: %+v", r)
	}
	if r.URL != "https://dev.azure.com/acme/Platform%20Tools/_git/api" || r.CloneURL != r.URL || r.SSHURL == "" {
		t.Errorf("URLs: %+v", r)
	}
	if r.DefaultBranch != "main" || r.Visibility != "private" || !r.Fork {
		t.Errorf("metadata: %+v", r)
	}
}

func TestRemoteParsersRejectInvalidJSON(t *testing.T) {
	if _, err := parseGitHubRepos("not json"); err == nil || !strings.Contains(err.Error(), "gh api user/repos") {
		t.Errorf("github error = %v", err)
	}
	if _, err := parseGitLabRepos("not json"); err == nil || !strings.Contains(err.Error(), "glab repo list") {
		t.Errorf("gitlab error = %v", err)
	}
	if _, err := parseAzureDevOpsRepos("not json"); err == nil || !strings.Contains(err.Error(), "az repos list") {
		t.Errorf("Azure DevOps error = %v", err)
	}
}

func TestRemoteRepoLabel(t *testing.T) {
	r := RemoteRepo{Forge: GitHub, FullName: "owner/repo", UpdatedAt: time.Now()}
	if got := r.Label(); got != "github:owner/repo" {
		t.Errorf("Label = %q", got)
	}
}
