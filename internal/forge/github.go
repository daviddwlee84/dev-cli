package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// gh drives the GitHub CLI.
type gh struct{}

func (g *gh) Kind() Kind      { return GitHub }
func (g *gh) Bin() string     { return "gh" }
func (g *gh) Available() bool { return have("gh") }

// CreatePR opens a GitHub pull request.
func (g *gh) CreatePR(ctx context.Context, dir string, req PRRequest) (string, error) {
	if !g.Available() {
		return "", &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	args := []string{"pr", "create", "--base", req.Base, "--head", req.Head}
	switch {
	case req.Title != "":
		args = append(args, "--title", req.Title, "--body", req.Body)
	default:
		// --fill derives title and body from the branch's commits, which is
		// the right default for a solo workflow where the commits already say
		// what happened.
		args = append(args, "--fill")
	}
	if req.Draft {
		args = append(args, "--draft")
	}
	if req.Web {
		args = append(args, "--web")
	}
	out, err := run(ctx, "gh", dir, args...)
	return lastURL(out), err
}

// CreateRepo creates a GitHub repository from the local checkout.
func (g *gh) CreateRepo(ctx context.Context, dir string, req RepoRequest) (string, error) {
	if !g.Available() {
		return "", &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	remote := req.RemoteName
	if remote == "" {
		remote = "origin"
	}
	args := []string{"repo", "create", req.Name, "--source", ".", "--remote", remote}
	if req.Private {
		args = append(args, "--private")
	} else {
		args = append(args, "--public")
	}
	if req.Description != "" {
		args = append(args, "--description", req.Description)
	}
	if req.Push {
		args = append(args, "--push")
	}
	out, err := run(ctx, "gh", dir, args...)
	return lastURL(out), err
}

// CloneURL renders a clone target. gh accepts a bare owner/name, so a
// shorthand reference is passed through untouched.
func (g *gh) CloneURL(ref string) string {
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "git@") {
		return ref
	}
	return "https://github.com/" + strings.TrimSuffix(ref, ".git") + ".git"
}

// lastURL picks the URL out of a CLI's chatter, which may print progress
// lines before the created resource's address.
func lastURL(out string) string {
	var found string
	for _, line := range strings.Fields(out) {
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			found = line
		}
	}
	if found == "" {
		return strings.TrimSpace(out)
	}
	return found
}

// ListRepos lists GitHub repositories visible to the authenticated user —
// owned, collaborated on, and organisation membership — most recently pushed
// first. gh api is intentionally used instead of a custom HTTP client:
// authentication and token refresh stay owned by the forge CLI.
func (g *gh) ListRepos(ctx context.Context, limit int) ([]RemoteRepo, error) {
	if !g.Available() {
		return nil, &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if limit <= 0 {
		limit = 100
	}
	// GitHub caps per_page at 100. The remote view is a navigator, not a
	// complete backup inventory; / filters this recent working set.
	if limit > 100 {
		limit = 100
	}
	out, err := run(ctx, "gh", "", "api", "user/repos", "--method", "GET",
		"-f", "per_page="+strconv.Itoa(limit), "-f", "sort=pushed", "-f", "direction=desc")
	if err != nil {
		return nil, err
	}
	return parseGitHubRepos(out)
}

func parseGitHubRepos(out string) ([]RemoteRepo, error) {
	var raw []struct {
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		Description   string `json:"description"`
		URL           string `json:"html_url"`
		CloneURL      string `json:"clone_url"`
		SSHURL        string `json:"ssh_url"`
		Visibility    string `json:"visibility"`
		Fork          bool   `json:"fork"`
		Archived      bool   `json:"archived"`
		PushedAt      string `json:"pushed_at"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("decode gh api user/repos: %w", err)
	}
	result := make([]RemoteRepo, 0, len(raw))
	for _, r := range raw {
		updated, _ := time.Parse(time.RFC3339, r.PushedAt)
		result = append(result, RemoteRepo{
			Forge: GitHub, Name: r.Name, FullName: r.FullName,
			Description: r.Description, URL: r.URL,
			CloneURL: r.CloneURL, SSHURL: r.SSHURL,
			Visibility: r.Visibility, DefaultBranch: r.DefaultBranch,
			Archived: r.Archived, Fork: r.Fork, UpdatedAt: updated,
		})
	}
	return result, nil
}
