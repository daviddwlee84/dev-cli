package repo

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// ValidateNavigation checks a repository row against current local filesystem and
// Git observations. An empty checkoutPath selects expected.Path; otherwise it
// selects an exact registered checkout of that repository. The returned path is
// the original navigation spelling, including any bootstrap symlink alias.
//
// Only identity fields present in expected can be compared. These path-based
// observations are not durable identity witnesses and cannot detect a repository
// deleted and recreated with identical paths. Validation neither mutates state
// nor guarantees that the target remains unchanged after this call returns.
func ValidateNavigation(ctx context.Context, expected Repo, checkoutPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Never let an inventory cycle's cached worktree list authorize navigation.
	ctx = gitx.WithReadOnlyObservations(gitx.WithObservations(ctx))
	if err := ctx.Err(); err != nil {
		return "", err
	}

	parent, err := navigationDirectory(expected.Path)
	if err != nil {
		return "", err
	}
	if err := navigationIdentity("repository target", parent, expected.RealPath); err != nil {
		return "", err
	}
	gitKnown := expected.HasGit || expected.CommonDir != "" || expected.GitDir != "" || expected.MainRoot != "" || expected.Bare
	if !gitKnown && checkoutPath == "" {
		// An explicitly observed plain directory remains a valid navigation
		// target, even when Git would discover a repository in an ancestor.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return expected.Path, nil
	}

	repository, err := discoverNavigationRepository(ctx, parent, "")
	if err != nil {
		return "", fmt.Errorf("validate repository %q: %w", expected.Path, err)
	}
	for _, identity := range []struct{ label, actual, expected string }{
		{"Git common directory", repository.GitCommonDir, expected.CommonDir},
		{"Git directory", repository.GitDir, expected.GitDir},
		{"main checkout", repository.MainRoot, expected.MainRoot},
	} {
		if err := navigationIdentity(identity.label, identity.actual, identity.expected); err != nil {
			return "", err
		}
	}
	if expected.Bare && !repository.Bare {
		return "", fmt.Errorf("repository %q is no longer bare", expected.Path)
	}
	if checkoutPath == "" {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return expected.Path, nil
	}

	checkout, err := navigationDirectory(checkoutPath)
	if err != nil {
		return "", err
	}
	registered, err := gitx.ResolveRegisteredWorktree(ctx, parent, checkout)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("validate checkout %q: %w", checkoutPath, err)
	}
	if registered.Worktree.Prunable || registered.Worktree.Bare {
		return "", fmt.Errorf("checkout %q is prunable or bare", checkoutPath)
	}
	if err := navigationIdentity("registered Git common directory", registered.GitCommonDir, repository.GitCommonDir); err != nil {
		return "", err
	}
	if err := navigationIdentity("registered main checkout", registered.RepositoryPath, repository.MainRoot); err != nil {
		return "", err
	}
	bareCommonDir := ""
	if repository.Bare {
		bareCommonDir = repository.GitCommonDir
	}
	selected, err := discoverNavigationRepository(ctx, checkout, bareCommonDir)
	if err != nil {
		return "", fmt.Errorf("validate checkout %q: %w", checkoutPath, err)
	}
	if err := navigationIdentity("checkout Git common directory", selected.GitCommonDir, repository.GitCommonDir); err != nil {
		return "", err
	}
	if err := navigationIdentity("checkout main root", selected.MainRoot, repository.MainRoot); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return checkoutPath, nil
}

func navigationDirectory(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect navigation directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("navigation target %q is not a directory", path)
	}
	canonical, err := pathx.Canonical(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize navigation directory %q: %w", path, err)
	}
	return canonical, nil
}

// navigationIdentity compares known paths, including native case/path aliases.
// SameFile compares current paths, not an earlier directory identity snapshot.
func navigationIdentity(label, actual, expected string) error {
	if expected == "" {
		return nil
	}
	canonical, err := pathx.Canonical(expected)
	if err != nil {
		return fmt.Errorf("canonicalize expected %s: %w", label, err)
	}
	if actual == canonical {
		return nil
	}
	actualInfo, actualErr := os.Stat(actual)
	expectedInfo, expectedErr := os.Stat(canonical)
	if actualErr == nil && expectedErr == nil && os.SameFile(actualInfo, expectedInfo) {
		return nil
	}
	return fmt.Errorf("%s changed: expected %q, found %q", label, canonical, actual)
}

func discoverNavigationRepository(ctx context.Context, path, bareCommonDir string) (gitx.Repo, error) {
	repository, err := gitx.Discover(ctx, path)
	if ctx.Err() != nil {
		return gitx.Repo{}, ctx.Err()
	}
	if err != nil && isBareDir(path) {
		// Discover requests --show-toplevel, which Git rejects for a bare hub.
		// Verify its identity with Git rather than accepting the skeleton alone.
		out, probeErr := gitx.RunReadOnly(ctx, path, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--is-bare-repository")
		if ctx.Err() != nil {
			return gitx.Repo{}, ctx.Err()
		}
		if probeErr != nil {
			return gitx.Repo{}, probeErr
		}
		fields := strings.Split(out, "\n")
		if len(fields) != 3 || fields[2] != "true" {
			return gitx.Repo{}, fmt.Errorf("cannot verify bare repository %q", path)
		}
		gitDir, canonicalErr := pathx.Canonical(fields[0])
		if canonicalErr != nil {
			return gitx.Repo{}, canonicalErr
		}
		commonDir, canonicalErr := pathx.Canonical(fields[1])
		if canonicalErr != nil {
			return gitx.Repo{}, canonicalErr
		}
		repository = gitx.Repo{GitDir: gitDir, GitCommonDir: commonDir, MainRoot: commonDir, Bare: true}
		err = nil
	} else if err != nil && bareCommonDir != "" {
		// Discover assumes a linked checkout's main root is a working tree.
		// Here the caller has freshly verified both the bare hub and this
		// checkout's registration, so probe the checkout's exact Git identity
		// without asking Discover to find a non-bare main checkout.
		out, probeErr := gitx.RunReadOnly(ctx, path, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
		if ctx.Err() != nil {
			return gitx.Repo{}, ctx.Err()
		}
		if probeErr != nil {
			return gitx.Repo{}, probeErr
		}
		fields := strings.Split(out, "\n")
		if len(fields) != 3 {
			return gitx.Repo{}, fmt.Errorf("cannot verify checkout %q of bare repository", path)
		}
		for i := range fields {
			fields[i], probeErr = pathx.Canonical(fields[i])
			if probeErr != nil {
				return gitx.Repo{}, probeErr
			}
		}
		if err := navigationIdentity("checkout Git common directory", fields[2], bareCommonDir); err != nil {
			return gitx.Repo{}, err
		}
		repository = gitx.Repo{Root: fields[0], GitDir: fields[1], GitCommonDir: fields[2], MainRoot: bareCommonDir, IsLinkedWorktree: fields[1] != fields[2]}
		err = nil
	}
	if err != nil {
		return gitx.Repo{}, err
	}
	root := repository.Root
	if repository.Bare {
		root = repository.GitCommonDir
	}
	// Discover also succeeds inside a checkout; navigation must validate the
	// selected directory itself, not silently accept an enclosing repository.
	if err := navigationIdentity("Git root", root, path); err != nil {
		return gitx.Repo{}, err
	}
	return repository, nil
}
