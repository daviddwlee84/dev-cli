package forge

import (
	"context"
	"strings"
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
