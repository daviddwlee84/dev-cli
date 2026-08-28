package diskusage

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// Scanner measures filesystem state. Clock is injectable for cache and JSON
// tests; the zero value uses time.Now.
type Scanner struct {
	Clock func() time.Time
}

// Scan uses the default scanner.
func Scan(ctx context.Context, target Target) (Usage, error) {
	return Scanner{}.Scan(ctx, target)
}

// Scan measures one target without following symlinks.
func (s Scanner) Scan(ctx context.Context, target Target) (Usage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if target.WorktreeCount < 0 {
		return Usage{}, fmt.Errorf("negative linked worktree count")
	}
	checkout, err := canonicalOptional(target.Checkout)
	if err != nil {
		return Usage{}, fmt.Errorf("canonicalize checkout: %w", err)
	}
	gitDir, err := canonicalOptional(target.GitDir)
	if err != nil {
		return Usage{}, fmt.Errorf("canonicalize Git directory: %w", err)
	}
	commonDir, err := canonicalOptional(target.CommonDir)
	if err != nil {
		return Usage{}, fmt.Errorf("canonicalize Git common directory: %w", err)
	}
	if (gitDir == "") != (commonDir == "") {
		return Usage{}, fmt.Errorf("Git target requires both git_dir and common_dir")
	}

	usage := Usage{Complete: true, MeasuredAt: s.now()}
	if !target.Bare {
		if checkout == "" {
			return Usage{}, fmt.Errorf("checkout path is required")
		}
		excludes := []string(nil)
		if gitDir != "" {
			excludes = append(excludes, filepath.Join(checkout, ".git"))
		}
		part, err := walkLogical(ctx, checkout, excludes)
		if err != nil {
			return Usage{}, fmt.Errorf("measure checkout %s: %w", checkout, err)
		}
		usage.CheckoutBytes = part.bytes
		mergePart(&usage, part)
	}

	switch {
	case commonDir == "":
		// Non-Git experiment: every byte below the checkout is privately owned.
	case target.Linked:
		if gitDir == "" || samePath(gitDir, commonDir) {
			return Usage{}, fmt.Errorf("linked worktree lacks a distinct Git directory")
		}
		private, err := walkLogical(ctx, gitDir, nil)
		if err != nil {
			return Usage{}, fmt.Errorf("measure linked-worktree Git directory %s: %w", gitDir, err)
		}
		usage.PrivateGitBytes = int64Pointer(private.bytes)
		mergePart(&usage, private)
		shared, err := walkLogical(ctx, commonDir, []string{gitDir})
		if err != nil {
			return Usage{}, fmt.Errorf("measure shared Git directory %s: %w", commonDir, err)
		}
		usage.SharedGitBytes = int64Pointer(shared.bytes)
		mergePart(&usage, shared)
	case target.WorktreeCount == 0 && samePath(gitDir, commonDir) &&
		(target.Bare || isWithinOrEqual(checkout, commonDir)):
		private, err := walkLogical(ctx, commonDir, nil)
		if err != nil {
			return Usage{}, fmt.Errorf("measure private Git directory %s: %w", commonDir, err)
		}
		usage.PrivateGitBytes = int64Pointer(private.bytes)
		mergePart(&usage, private)
	default:
		// A main checkout with linked worktrees, or a checkout using an external
		// Git directory, cannot claim the common object database as reclaimable.
		shared, err := walkLogical(ctx, commonDir, nil)
		if err != nil {
			return Usage{}, fmt.Errorf("measure shared Git directory %s: %w", commonDir, err)
		}
		usage.SharedGitBytes = int64Pointer(shared.bytes)
		mergePart(&usage, shared)
	}

	private := int64(0)
	if usage.PrivateGitBytes != nil {
		private = *usage.PrivateGitBytes
	}
	if usage.CheckoutBytes > math.MaxInt64-private {
		return Usage{}, fmt.Errorf("owned byte count overflows int64")
	}
	usage.OwnedBytes = usage.CheckoutBytes + private
	if usage.SharedGitBytes == nil {
		usage.TotalBytes = int64Pointer(usage.OwnedBytes)
	}
	if err := usage.Validate(); err != nil {
		return Usage{}, err
	}
	return usage, nil
}

func (s Scanner) now() time.Time {
	if s.Clock == nil {
		return time.Now().UTC()
	}
	return s.Clock().UTC()
}

type walkPart struct {
	bytes      int64
	complete   bool
	unreadable int
}

func walkLogical(ctx context.Context, root string, excludes []string) (walkPart, error) {
	part := walkPart{complete: true}
	cleanExcludes := make([]string, 0, len(excludes))
	for _, exclude := range excludes {
		if exclude != "" {
			cleanExcludes = append(cleanExcludes, filepath.Clean(exclude))
		}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		path = filepath.Clean(path)
		if path != root && excludedPath(path, cleanExcludes) {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			part.complete = false
			part.unreadable++
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root || entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			part.complete = false
			part.unreadable++
			return nil
		}
		size := info.Size()
		if size < 0 || part.bytes > math.MaxInt64-size {
			return fmt.Errorf("logical byte count overflows int64 at %s", path)
		}
		part.bytes += size
		return nil
	})
	if err != nil {
		return walkPart{}, err
	}
	return part, nil
}

func excludedPath(path string, excludes []string) bool {
	for _, exclude := range excludes {
		if path == exclude || strings.HasPrefix(path, exclude+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func mergePart(usage *Usage, part walkPart) {
	usage.Complete = usage.Complete && part.complete
	usage.UnreadableEntries += part.unreadable
}

func canonicalOptional(path string) (string, error) {
	if path == "" || path == "." {
		return "", nil
	}
	canonical, err := pathx.Canonical(path)
	if err != nil {
		return "", err
	}
	return canonical, nil
}

func isWithinOrEqual(root, candidate string) bool {
	if root == "" || candidate == "" {
		return false
	}
	if samePath(root, candidate) {
		return true
	}
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func samePath(left, right string) bool {
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}

func int64Pointer(value int64) *int64 { return &value }
