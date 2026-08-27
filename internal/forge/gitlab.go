package forge

import (
	"context"
	"strings"
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
