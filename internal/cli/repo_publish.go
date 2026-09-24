package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
	Remote        forge.CreateRepoResult `json:"remote"`
	Added         bool                   `json:"remote_added"`
	Pushed        bool                   `json:"pushed"`
	Branch        string                 `json:"branch,omitempty"`
	Partial       bool                   `json:"partial"`
	created       bool
	pushAttempted bool
}

// Graduation can pin the exact post-move checkout and commit. Other repository
// callers retain the established live-branch publishing behavior.
type repoPublishExpectation struct {
	Branch, Head string
	Validate     func() error
}

func publishRepository(ctx context.Context, root string, request repoPublishRequest) (repoPublishResult, error) {
	return publishRepositoryExpected(ctx, root, request, nil)
}

func publishRepositoryExpected(ctx context.Context, root string, request repoPublishRequest, expected *repoPublishExpectation) (repoPublishResult, error) {
	adapter, err := forge.For(request.Forge)
	if err != nil {
		return repoPublishResult{}, err
	}
	return publishRepositoryWithForgeExpected(ctx, root, adapter, request, expected)
}

func publishRepositoryWithForge(ctx context.Context, root string, adapter forge.Forge, request repoPublishRequest) (repoPublishResult, error) {
	return publishRepositoryWithForgeExpected(ctx, root, adapter, request, nil)
}

func publishRepositoryWithForgeExpected(ctx context.Context, root string, adapter forge.Forge, request repoPublishRequest, expected *repoPublishExpectation) (repoPublishResult, error) {
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
	if expected != nil {
		if err := expected.Validate(); err != nil {
			return repoPublishResult{}, err
		}
	}
	created, err := forge.PublishRepo(ctx, adapter, root, forge.RepoRequest{
		Name: request.Name, Namespace: request.Namespace, Description: request.Description,
		Visibility: request.Visibility, RemoteName: remoteName,
	})
	result := repoPublishResult{Remote: created, Partial: err != nil, created: err == nil}
	if err != nil {
		return result, err
	}
	remoteURL := created.RemoteURL
	if remoteURL == "" {
		remoteURL = created.CloneURL
	}
	if remoteURL == "" {
		result.Partial = true
		return result, fmt.Errorf("%s created the repository but returned no clone URL", request.Forge)
	}
	return finishRepositoryUpstream(ctx, root, remoteName, remoteURL, request.Push, result, expected)
}

// attachRepositoryUpstream adds an explicitly supplied URL without invoking a
// forge. It shares the local remote/push steps with repository publication.
func attachRepositoryUpstream(ctx context.Context, root, remoteURL string, push bool) (repoPublishResult, error) {
	return attachRepositoryUpstreamExpected(ctx, root, remoteURL, push, nil)
}

func attachRepositoryUpstreamExpected(ctx context.Context, root, remoteURL string, push bool, expected *repoPublishExpectation) (repoPublishResult, error) {
	result := repoPublishResult{Remote: forge.CreateRepoResult{RemoteName: "origin", RemoteURL: remoteURL, CloneURL: remoteURL}}
	if current := gitx.Remote(ctx, root, "origin"); current != "" {
		return result, fmt.Errorf("remote origin already exists; refusing to replace it")
	}
	return finishRepositoryUpstream(ctx, root, "origin", remoteURL, push, result, expected)
}

func finishRepositoryUpstream(ctx context.Context, root, remoteName, remoteURL string, push bool, result repoPublishResult, expected *repoPublishExpectation) (repoPublishResult, error) {
	if expected != nil {
		if err := expected.Validate(); err != nil {
			result.Partial = true
			return result, err
		}
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
	if !push {
		return result, nil
	}
	if expected != nil {
		if err := expected.Validate(); err != nil {
			result.Partial = true
			return result, err
		}
		if err := verifyPublishRemote(ctx, root, remoteName, remoteURL); err != nil {
			result.Partial = true
			return result, err
		}
		result.Branch = expected.Branch
		result.pushAttempted = true
		if _, err := gitx.Run(ctx, root, "push", "--no-follow-tags", "--recurse-submodules=no", remoteName, expected.Head+":refs/heads/"+expected.Branch); err != nil {
			result.Partial = true
			return result, fmt.Errorf("push reviewed commit to %s/%s: %w", remoteName, expected.Branch, err)
		}
		result.Pushed = true
		if err := expected.Validate(); err != nil {
			result.Partial = true
			return result, fmt.Errorf("reviewed commit was pushed, but checkout changed before tracking setup: %w", err)
		}
		if _, err := gitx.Run(ctx, root, "branch", "--set-upstream-to="+remoteName+"/"+expected.Branch, expected.Branch); err != nil {
			result.Partial = true
			return result, fmt.Errorf("reviewed commit was pushed, but tracking setup failed: %w", err)
		}
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
	result.pushAttempted = true
	if _, err := gitx.Run(ctx, root, "push", "-u", remoteName, status.Branch); err != nil {
		result.Partial = true
		return result, fmt.Errorf("push %s to %s: %w", status.Branch, remoteName, err)
	}
	result.Pushed = true
	return result, nil
}

func verifyPublishRemote(ctx context.Context, root, remoteName, remoteURL string) error {
	for _, key := range []string{"url", "pushurl"} {
		value, err := gitx.Run(ctx, root, "config", "--get-all", "remote."+remoteName+"."+key)
		if err != nil {
			if key == "pushurl" && strings.TrimSpace(value) == "" && quietGitExit(err, 1) {
				// An absent push URL intentionally uses the verified fetch URL.
				continue
			}
			return fmt.Errorf("upstream configuration is unavailable before push")
		}
		if strings.TrimSpace(value) != remoteURL {
			return errors.New("upstream configuration changed before push")
		}
	}
	return nil
}

func quietGitExit(err error, code int) bool {
	var gitErr *gitx.Error
	var exitErr *exec.ExitError
	return errors.As(err, &gitErr) && errors.As(gitErr.Err, &exitErr) && exitErr.ExitCode() == code &&
		strings.TrimSpace(gitErr.Stderr) == "" && strings.TrimSpace(gitErr.Stdout) == ""
}
