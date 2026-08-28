package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// glab drives the GitLab CLI. GitLab calls the same concept a merge request,
// and its flags differ enough from gh that the two need separate adapters
// rather than a shared one with conditionals.
type glab struct{}

func (g *glab) Kind() Kind      { return GitLab }
func (g *glab) Bin() string     { return "glab" }
func (g *glab) Available() bool { return have("glab") }

// CreatePR opens a GitLab merge request.
func (g *glab) CreatePR(ctx context.Context, dir string, req PRRequest) (string, error) {
	if !g.Available() {
		return "", &ErrNoCLI{Kind: GitLab, Bin: "glab"}
	}
	args := []string{"mr", "create", "--target-branch", req.Base, "--source-branch", req.Head}
	switch {
	case req.Title != "":
		args = append(args, "--title", req.Title, "--description", req.Body)
	default:
		args = append(args, "--fill")
	}
	if req.Draft {
		args = append(args, "--draft")
	}
	if req.Web {
		args = append(args, "--web")
	}
	// Without this glab prompts interactively even when everything is supplied.
	args = append(args, "--yes")
	out, err := run(ctx, "glab", dir, args...)
	return lastURL(out), err
}

// CreateRepo creates a GitLab project from the local checkout.
func (g *glab) CreateRepo(ctx context.Context, dir string, req RepoRequest) (string, error) {
	if !g.Available() {
		return "", &ErrNoCLI{Kind: GitLab, Bin: "glab"}
	}
	remote := req.RemoteName
	if remote == "" {
		remote = "origin"
	}
	args := []string{"repo", "create", req.Name, "--remoteName", remote}
	if req.Private {
		args = append(args, "--private")
	} else {
		args = append(args, "--public")
	}
	if req.Description != "" {
		args = append(args, "--description", req.Description)
	}
	out, err := run(ctx, "glab", dir, args...)
	return lastURL(out), err
}

// CloneURL renders a clone target.
func (g *glab) CloneURL(ref string) string {
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "git@") {
		return ref
	}
	return "https://gitlab.com/" + strings.TrimSuffix(ref, ".git") + ".git"
}

// ListRepos lists every GitLab project of which the authenticated user is a
// member, most recently active first. glab owns authentication and self-hosted
// instance selection; dev only normalises the JSON into the shared shape.
func (g *glab) ListRepos(ctx context.Context) ([]RemoteRepo, error) {
	if !g.Available() {
		return nil, &ErrNoCLI{Kind: GitLab, Bin: "glab"}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	const pageSize = 100
	var result []RemoteRepo
	for page := 1; ; page++ {
		out, err := run(ctx, "glab", "", "api", "projects", "--method", "GET",
			"-f", "membership=true", "-f", "simple=true", "-f", "per_page=100",
			"-f", fmt.Sprintf("page=%d", page), "-f", "order_by=last_activity_at", "-f", "sort=desc")
		if err != nil {
			return result, err
		}
		repos, err := parseGitLabRepos(out)
		if err != nil {
			return result, err
		}
		result = append(result, repos...)
		if len(repos) < pageSize {
			break
		}
	}
	return result, nil
}

func parseGitLabRepos(out string) ([]RemoteRepo, error) {
	var raw []struct {
		Name              string `json:"name"`
		PathWithNamespace string `json:"path_with_namespace"`
		Description       string `json:"description"`
		DefaultBranch     string `json:"default_branch"`
		Visibility        string `json:"visibility"`
		SSHURL            string `json:"ssh_url_to_repo"`
		HTTPURL           string `json:"http_url_to_repo"`
		WebURL            string `json:"web_url"`
		Archived          bool   `json:"archived"`
		ForkedFrom        any    `json:"forked_from_project"`
		LastActivityAt    string `json:"last_activity_at"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("decode glab api projects: %w", err)
	}
	result := make([]RemoteRepo, 0, len(raw))
	for _, r := range raw {
		updated, _ := time.Parse(time.RFC3339Nano, r.LastActivityAt)
		result = append(result, RemoteRepo{
			Forge: GitLab, Name: r.Name, FullName: r.PathWithNamespace,
			Description: r.Description, URL: r.WebURL,
			CloneURL: r.HTTPURL, SSHURL: r.SSHURL,
			Visibility: r.Visibility, DefaultBranch: r.DefaultBranch,
			Archived: r.Archived, Fork: r.ForkedFrom != nil, UpdatedAt: updated,
		})
	}
	return result, nil
}
