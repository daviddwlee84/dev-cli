package taskflow

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

// Rebase may materialize the upstream tree and any replayed commit. Git's
// checkout machinery can overwrite ignored files, unlike our FF merge's
// --no-overwrite-ignore path. Reject every file/directory-prefix collision.
func syncRebaseIgnoredCollisions(ctx context.Context, path string, branch gitx.BranchState, contents gitx.CheckoutContents) error {
	if len(contents.Ignored) == 0 {
		return nil
	}
	commits, err := gitx.Run(ctx, path, "rev-list", branch.UpstreamOID+".."+branch.OID, "--")
	if err != nil {
		return fmt.Errorf("cannot inspect rebase commits: %w", err)
	}
	oids := append([]string{branch.UpstreamOID}, strings.Fields(commits)...)
	if len(oids) > 1000 {
		return fmt.Errorf("rebase has too many commits to verify ignored-file safety")
	}
	for _, oid := range oids {
		paths, err := gitx.Run(ctx, path, "ls-tree", "-r", "--name-only", "-z", oid)
		if err != nil {
			return fmt.Errorf("cannot inspect rebase tree: %w", err)
		}
		for _, tracked := range strings.Split(paths, "\x00") {
			if tracked == "" {
				continue
			}
			for _, ignored := range contents.Ignored {
				local := strings.TrimSuffix(filepath.ToSlash(ignored.Path), "/")
				// Conservative on every host, including case-sensitive Linux:
				// a plan must remain safe on macOS and Windows filesystems.
				if syncPathsOverlap(tracked, local) {
					return fmt.Errorf("rebase would overwrite ignored path %q; move it before synchronization", local)
				}
			}
		}
	}
	return nil
}

// Component comparison also handles Unicode folds whose UTF-8 byte lengths
// differ, so prefix protection does not rely on slicing arbitrary path bytes.
func syncPathsOverlap(left, right string) bool {
	a := strings.Split(strings.TrimSuffix(filepath.ToSlash(left), "/"), "/")
	b := strings.Split(strings.TrimSuffix(filepath.ToSlash(right), "/"), "/")
	for i := 0; i < len(a) && i < len(b); i++ {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}
