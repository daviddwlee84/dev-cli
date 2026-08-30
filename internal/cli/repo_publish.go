package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

type repoPublishRequest struct {
	Forge       forge.Kind       `json:"forge"`
	Name        string           `json:"name"`
	Namespace   string           `json:"namespace,omitempty"`
	Description string           `json:"description,omitempty"`
	Visibility  forge.Visibility `json:"visibility"`
	RemoteName  string           `json:"remote_name,omitempty"`
	Push        bool             `json:"push"`
}

type repoPublishResult struct {
	Remote  forge.CreateRepoResult `json:"remote"`
	Added   bool                   `json:"remote_added"`
	Pushed  bool                   `json:"pushed"`
	Branch  string                 `json:"branch,omitempty"`
	Partial bool                   `json:"partial"`
}

func publishRepository(ctx context.Context, root string, request repoPublishRequest) (repoPublishResult, error) {
	adapter, err := forge.For(request.Forge)
	if err != nil {
		return repoPublishResult{}, err
	}
	return publishRepositoryWithForge(ctx, root, adapter, request)
}

func publishRepositoryWithForge(ctx context.Context, root string, adapter forge.Forge, request repoPublishRequest) (repoPublishResult, error) {
	remoteName := strings.TrimSpace(request.RemoteName)
	if remoteName == "" {
		remoteName = "origin"
	}
	if current := gitx.Remote(ctx, root, remoteName); current != "" {
		return repoPublishResult{}, fmt.Errorf("remote %s already points to %s; refusing to create another upstream", remoteName, current)
	}
	readiness := forge.ProbeForge(ctx, adapter)
	if !readiness.Ready() {
		return repoPublishResult{}, fmt.Errorf("%s is not ready: %s; %s", request.Forge, readiness.Detail, readiness.Action)
	}
	created, err := forge.PublishRepo(ctx, adapter, root, forge.RepoRequest{
		Name: request.Name, Namespace: request.Namespace, Description: request.Description,
		Visibility: request.Visibility, RemoteName: remoteName,
	})
	result := repoPublishResult{Remote: created, Partial: err != nil}
	if err != nil {
		return result, err
	}
	remoteURL := created.RemoteURL
	if remoteURL == "" {
		remoteURL = created.CloneURL
	}
	if remoteURL == "" {
		return result, fmt.Errorf("%s created the repository but returned no clone URL", request.Forge)
	}
	if current := gitx.Remote(ctx, root, remoteName); current != "" {
		if current != remoteURL {
			result.Partial = true
			return result, fmt.Errorf("remote %s already points to %s (created upstream is %s)", remoteName, current, remoteURL)
		}
	} else if _, err := gitx.Run(ctx, root, "remote", "add", remoteName, remoteURL); err != nil {
		result.Partial = true
		return result, fmt.Errorf("add remote %s: %w", remoteName, err)
	} else {
		result.Added = true
	}
	if !request.Push {
		return result, nil
	}
	status, err := gitx.StatusOf(ctx, root)
	if err != nil {
		result.Partial = true
		return result, err
	}
	if status.Branch == "" || status.Branch == "HEAD" {
		result.Partial = true
		return result, fmt.Errorf("cannot push a detached checkout")
	}
	result.Branch = status.Branch
	if _, err := gitx.Run(ctx, root, "push", "-u", remoteName, status.Branch); err != nil {
		result.Partial = true
		return result, fmt.Errorf("push %s to %s: %w", status.Branch, remoteName, err)
	}
	result.Pushed = true
	return result, nil
}
