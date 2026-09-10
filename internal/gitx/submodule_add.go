package gitx

import (
	"context"
	"fmt"
	"strings"
)

// CloneSubmodule creates a private, absorbed-layout clone. Unlike submodule add,
// it does not register URL/active keys in the superproject's shared config.
func CloneSubmodule(ctx context.Context, parent, source, target, gitdir string) error {
	_, err := Run(ctx, parent, "clone", "--no-checkout", "--no-local", "--separate-git-dir", gitdir, "--", source, target)
	if err != nil {
		// Git may include credentials from URL rewrite configuration in stderr.
		return fmt.Errorf("clone failed; inspect retained checkout %s and Git directory %s", target, gitdir)
	}
	_, err = Run(ctx, target, "config", "--local", "core.worktree", target)
	return err
}

// CheckoutAddedSubmodule resolves against the freshly cloned repository, never
// against stale provider metadata or the parent's current branch.
func CheckoutAddedSubmodule(ctx context.Context, target, mode, ref string) (head, branch string, err error) {
	if mode == "default-branch" || ref == "" {
		var remoteHead string
		remoteHead, err = Run(ctx, target, "symbolic-ref", "refs/remotes/origin/HEAD")
		if err != nil || !strings.HasPrefix(remoteHead, "refs/remotes/origin/") {
			return "", "", fmt.Errorf("remote default branch is unavailable; retain clone and specify a pinned --ref")
		}
		branch = strings.TrimPrefix(remoteHead, "refs/remotes/origin/")
		ref = remoteHead
	}
	head, err = Run(ctx, target, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil && !strings.HasPrefix(ref, "refs/") {
		head, err = Run(ctx, target, "rev-parse", "--verify", "--end-of-options", "refs/remotes/origin/"+ref+"^{commit}")
	}
	if err != nil {
		return "", "", fmt.Errorf("requested ref does not resolve to a fetched commit")
	}
	if mode == "default-branch" {
		_, err = Run(ctx, target, "checkout", "-B", branch, head)
		if err == nil {
			_, err = Run(ctx, target, "branch", "--set-upstream-to=origin/"+branch, "--", branch)
		}
	} else {
		branch = ""
		_, err = Run(ctx, target, "checkout", "--detach", head)
	}
	return head, branch, err
}

// StageAddedSubmodule uses literal pathspecs and Git's own atomic index lock;
// no unrelated staged or working-tree paths are included.
func StageAddedSubmodule(ctx context.Context, root, path string) error {
	_, err := Run(ctx, root, "--literal-pathspecs", "add", "--", ".gitmodules", path)
	return err
}
