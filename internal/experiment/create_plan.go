package experiment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// CreatePlan freezes one dated acquisition destination. Unlike the legacy
// convenience Create call, Apply never silently selects a different suffix.
type CreatePlan struct {
	Request        CreateRequest `json:"request"`
	Path           string        `json:"path"`
	CloneRef       string        `json:"clone_ref,omitempty"`
	parent         string
	parentIdentity string
	root           string
	host           string
	seal           string
}

func createPlanDigest(p CreatePlan) string {
	b, _ := json.Marshal([]any{p.Request, p.Path, p.CloneRef, p.parent, p.parentIdentity, p.root, p.host})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// PlanCreate is read-only: even a missing tries_root is created only at Apply.
func (s *Service) PlanCreate(ctx context.Context, req CreateRequest) (CreatePlan, error) {
	var p CreatePlan
	if _, problems, err := s.store.ListWithDiagnostics(); err != nil {
		return p, err
	} else if len(problems) != 0 {
		return p, incompleteCatalogError(problems)
	}
	if req.Clone != "" && req.NoGit {
		return p, errors.New("clone and no-git are mutually exclusive")
	}
	name := req.Name
	if name == "" && req.Clone != "" {
		name = cloneDefaultName(req.Clone)
	}
	slug := config.Slug(strings.ReplaceAll(name, " ", "-"))
	if err := pathx.ValidateComponent(slug); err != nil {
		return p, err
	}
	path, err := s.availableDatedPath(slug)
	if err != nil {
		return p, err
	}
	parent, err := nearestExisting(filepath.Dir(path))
	if err != nil {
		return p, err
	}
	parent, err = pathx.Canonical(parent)
	if err != nil {
		return p, err
	}
	identity, err := gitx.DirectoryIdentity(parent)
	if err != nil {
		return p, err
	}
	if _, discoverErr := gitx.Discover(ctx, parent); discoverErr == nil {
		return p, errors.New("Try acquisition must be outside an existing Git checkout")
	}
	p = CreatePlan{Request: req, Path: path, parent: parent, parentIdentity: identity, root: s.triesRoot, host: s.host}
	if req.Clone != "" {
		p.CloneRef, err = s.normalizeCloneRef(req.Clone)
		if err != nil {
			return CreatePlan{}, err
		}
	}
	p.seal = createPlanDigest(p)
	return p, nil
}

func (s *Service) checkCreatePlan(p CreatePlan) error {
	if p.seal == "" || p.seal != createPlanDigest(p) || p.root != s.triesRoot || p.host != s.host {
		return errors.New("Try creation plan changed; preview again")
	}
	identity, err := gitx.DirectoryIdentity(p.parent)
	if err != nil || identity != p.parentIdentity {
		return errors.New("Try creation parent changed; preview again")
	}
	if err := s.validateVisibleTryPath(p.Path); err != nil {
		return err
	}
	return rejectExisting(p.Path)
}

// ApplyCreate retains partial filesystem effects and enrolls only the selected
// successful clone. It never reconciles unrelated Try folders or move intents.
func (s *Service) ApplyCreate(ctx context.Context, p CreatePlan) (CreateResult, error) {
	result := CreateResult{Path: p.Path}
	if err := s.checkCreatePlan(p); err != nil {
		return result, err
	}
	err := lockx.WithDir(ctx, filepath.Join(s.store.Dir, ".try-create-"+p.seal[:16]), "Try acquisition", func() error {
		if err := s.checkCreatePlan(p); err != nil {
			return err
		}
		if err := s.ensureTriesRoot(); err != nil {
			return err
		}
		if err := os.Mkdir(p.Path, 0o755); err != nil {
			return err
		}
		result.Created = true
		identity, err := gitx.DirectoryIdentity(p.Path)
		if err != nil {
			return err
		}
		if p.CloneRef != "" {
			if _, err := s.gitRun(ctx, s.triesRoot, "clone", "--origin", "origin", "--no-recurse-submodules", "--", p.CloneRef, p.Path); err != nil {
				return fmt.Errorf("partial Try retained at %s: %w", p.Path, err)
			}
			result.Cloned = true
		} else if !p.Request.NoGit {
			if _, err := s.gitRun(ctx, p.Path, "init", "-b", "main"); err != nil {
				result.InitWarning = err
			}
		}
		fresh, err := gitx.DirectoryIdentity(p.Path)
		if err != nil || fresh != identity {
			return errors.New("Try directory identity changed during acquisition; inspect retained path")
		}
		probe := s.probeDirectory(ctx, p.Path)
		if !probe.valid || probe.live.DiscoverError != nil {
			return errors.New("Try exists but cannot be verified for catalog enrollment")
		}
		return s.store.WithLock(ctx, func() error {
			entries, problems, err := s.store.ListWithDiagnostics()
			if err != nil {
				return err
			}
			if len(problems) != 0 {
				return incompleteCatalogError(problems)
			}
			if len(matchingEntries(entries, s.host, probe)) != 0 {
				return errors.New("new Try path is already claimed; inspect retained clone")
			}
			entry := s.newEntry(probe)
			if err := s.catalogCreate(entry); err != nil {
				return err
			}
			result.Item, result.Tracked = itemFromEntry(entry, probe.live), true
			return nil
		})
	})
	return result, err
}
