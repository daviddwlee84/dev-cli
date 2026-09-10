package submodule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// Recover restores interrupted cleanup. A previous remote proof never
// authorizes deletion of the quarantined data during recovery.
func Recover(ctx context.Context, journalPath string, check func(context.Context, string) error, dryRun bool) error {
	journalPath, err := filepath.Abs(journalPath)
	if err != nil {
		return err
	}
	area := filepath.Dir(journalPath)
	if filepath.Base(journalPath) != "journal.json" || !strings.HasPrefix(filepath.Base(area), ".dev-submodule-retirement-") {
		return errors.New("not a submodule removal journal")
	}
	held, areaIdentity, err := safefile.OpenRoot(area)
	if err != nil {
		return err
	}
	info, err := held.Lstat("journal.json")
	if err != nil {
		held.Close()
		return err
	}
	data, _, err := safefile.ReadStableRegular(ctx, held, "journal.json", info, 1024*1024)
	held.Close()
	if err != nil {
		return err
	}
	area, err = pathx.Canonical(area)
	if err != nil {
		return err
	}
	if err := safefile.VerifyRoot(area, areaIdentity); err != nil {
		return err
	}
	var j removalJournal
	if err = json.Unmarshal(data, &j); err != nil {
		return err
	}
	if j.Version != 1 || j.Root == "" || j.GitDir == "" || j.Repository == "" || j.Branch == "" || j.Head == "" {
		return errors.New("incomplete removal journal")
	}
	if filepath.Dir(j.Root) != filepath.Dir(area) {
		return errors.New("journal is not beside its recorded worktree")
	}
	r, err := gitx.Discover(ctx, j.Repository)
	if err != nil {
		return err
	}
	if inside, err := pathx.Contains(r.MainRoot, j.Root); err != nil || inside {
		return errors.New("recovery target is inside the canonical repository")
	}
	if inside, err := pathx.Contains(filepath.Join(r.GitCommonDir, "worktrees"), j.GitDir); err != nil || !inside {
		return errors.New("journal Git directory is not private worktree metadata")
	}
	for _, m := range j.Moves {
		if filepath.Dir(m.To) != area || (!strings.HasPrefix(filepath.Base(m.To), "checkout-") && filepath.Base(m.To) != "git-modules") {
			return errors.New("journal contains an unsafe quarantine path")
		}
		inside, err := pathx.Contains(j.Root, m.From)
		if err != nil {
			return err
		}
		if m.From == j.Root || (!inside && m.From != filepath.Join(j.GitDir, "modules")) {
			return errors.New("journal contains an unsafe restore path")
		}
	}
	if dryRun {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if inside, err := pathx.Contains(j.Root, cwd); err != nil || inside {
		return errors.New("recover from outside the target worktree")
	}
	return gitx.WithLifecycleLock(ctx, r.GitCommonDir, func() error {
		if check != nil {
			if err := check(ctx, j.Root); err != nil {
				return err
			}
		}
		if _, err := os.Lstat(j.Root); os.IsNotExist(err) {
			head, err := gitx.Run(ctx, r.MainRoot, "rev-parse", "refs/heads/"+j.Branch)
			if err != nil || head != j.Head {
				return errors.New("retained outer branch changed; recovery requires inspection")
			}
			if err := gitx.AddWorktree(ctx, r.MainRoot, j.Root, j.Branch, j.Head); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		current, err := gitx.Discover(ctx, j.Root)
		if err != nil {
			return err
		}
		head, err := gitx.Run(ctx, j.Root, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if !current.IsLinkedWorktree || current.GitDir != j.GitDir || head != j.Head {
			return errors.New("original worktree identity was reused; quarantine retained")
		}
		for i := len(j.Moves) - 1; i >= 0; i-- {
			if err := safefile.VerifyRoot(area, areaIdentity); err != nil {
				return err
			}
			m := j.Moves[i]
			info, err := os.Lstat(m.To)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("quarantined directory identity changed")
			}
			if existing, err := os.Lstat(m.From); err == nil {
				if !m.Empty || !existing.IsDir() || existing.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("restore path reused: %s; quarantine retained", m.From)
				}
				if err := os.Remove(m.From); err != nil {
					return fmt.Errorf("restore path is not empty: %s; quarantine retained", m.From)
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := os.Rename(m.To, m.From); err != nil {
				return fmt.Errorf("restore %s: %w; journal retained", m.From, err)
			}
		}
		if _, err := gitx.SubmodulesOf(ctx, j.Root); err != nil {
			return err
		}
		if err := safefile.VerifyRoot(area, areaIdentity); err != nil {
			return err
		}
		return os.RemoveAll(area)
	})
}
