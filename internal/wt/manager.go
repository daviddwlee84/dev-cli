package wt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

// Manager creates and removes worktrees according to the configured policy.
type Manager struct {
	Cfg config.Config
	// Runtime surfaces a new checkout in the multiplexer. May be nil.
	Runtime runtime.Runtime
	Log     io.Writer
}

// CreateRequest describes one worktree to create.
type CreateRequest struct {
	// RepoPath is the main checkout that owns the worktree.
	RepoPath string
	// RepoName is used in the path template and as the session label.
	RepoName string
	// Branch is the change stream. Created from Base when it does not exist.
	Branch string
	// Base is the ref a new branch starts from.
	//
	// Passing this explicitly matters more than it looks: without a base, git
	// branches from the current HEAD, so a worktree created while standing on
	// feature/A silently inherits feature/A's commits.
	Base string
	// Category feeds the path template.
	Category string
	// Path overrides the templated location entirely.
	Path string
	// Label names the runtime session.
	Label string
	// NoProvision skips dependency install and gitignored-file copying.
	NoProvision bool
	// NoRuntime skips surfacing the checkout in the multiplexer.
	NoRuntime bool
	// Focus opens the runtime session focused.
	Focus bool
}

// CreateResult reports what was created.
type CreateResult struct {
	Path          string
	Branch        string
	BranchCreated bool
	Provision     Result
	RuntimeHandle string
	RuntimeName   string
}

// ErrExists reports a worktree that is already checked out for that branch.
type ErrExists struct {
	Branch string
	Path   string
}

func (e *ErrExists) Error() string {
	return fmt.Sprintf("branch %s is already checked out at %s", e.Branch, e.Path)
}

func (m *Manager) logf(format string, args ...any) {
	if m.Log != nil {
		fmt.Fprintf(m.Log, format+"\n", args...)
	}
}

// Create adds a linked worktree, provisions it and surfaces it in the runtime.
func (m *Manager) Create(ctx context.Context, req CreateRequest) (*CreateResult, error) {
	if req.Branch == "" {
		return nil, errors.New("branch is required")
	}
	repo, err := gitx.Discover(ctx, req.RepoPath)
	if err != nil {
		return nil, err
	}
	// Always operate from the main checkout: creating a worktree from inside
	// another worktree works, but the relative paths and the base ref get
	// confusing fast.
	repoPath := repo.MainRoot
	if req.RepoName == "" {
		req.RepoName = repo.Name
	}

	// Git refuses to check one branch out twice, so an existing worktree for
	// this branch is a reuse, not an error the user needs to resolve.
	if existing, ok, err := gitx.WorktreeFor(ctx, repoPath, req.Branch); err == nil && ok {
		return nil, &ErrExists{Branch: req.Branch, Path: existing.Path}
	}

	path := req.Path
	if path == "" {
		path, err = m.Cfg.WorktreePathFor(req.RepoName, repoPath, req.Branch, req.Category)
		if err != nil {
			return nil, err
		}
	} else {
		path = config.Expand(path)
	}
	if err := m.checkTarget(path, repoPath); err != nil {
		return nil, err
	}

	base := req.Base
	branchExisted := gitx.BranchExists(ctx, repoPath, req.Branch)
	if !branchExisted {
		if base == "" {
			base = gitx.DefaultBranch(ctx, repoPath)
		}
		if base != "" && !gitx.RefExists(ctx, repoPath, base) {
			return nil, fmt.Errorf("base ref %q does not exist in %s", base, repoPath)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create worktree parent: %w", err)
	}
	if err := gitx.AddWorktree(ctx, repoPath, path, req.Branch, base); err != nil {
		return nil, err
	}
	m.logf("worktree %s -> %s", req.Branch, config.Contract(path))

	res := &CreateResult{Path: path, Branch: req.Branch, BranchCreated: !branchExisted}

	if !req.NoProvision {
		// SettingsFor folds in the repo's own .dev.toml, so a project can pin
		// its setup where a teammate on another machine picks it up too.
		p := &Provisioner{
			Settings: SettingsFor(m.Cfg, repoPath),
			Timeout:  m.Cfg.Worktree.ProvisionTimeout.Duration,
			Log:      m.Log,
		}
		pr, err := p.Provision(ctx, repoPath, path)
		if err != nil {
			// The checkout exists and is usable; provisioning is best-effort.
			m.logf("warning: provisioning: %v", err)
		}
		res.Provision = pr
	}

	if !req.NoRuntime && m.Runtime != nil {
		label := req.Label
		if label == "" {
			label = req.RepoName + "/" + req.Branch
		}
		handle, err := m.surface(ctx, path, label)
		if err != nil {
			m.logf("warning: could not open a runtime session: %v", err)
		} else if m.Runtime.Name() != "none" {
			res.RuntimeHandle, res.RuntimeName = handle, m.Runtime.Name()
		}
	}
	return res, nil
}

// surface registers the checkout with the runtime. When the backend
// understands git worktrees it is asked to open it as one, which is what makes
// the checkout appear grouped under its parent repo with its own branch and
// ahead/behind row rather than as an unrelated directory.
func (m *Manager) surface(ctx context.Context, path, label string) (string, error) {
	if wo, ok := m.Runtime.(runtime.WorktreeOpener); ok {
		return wo.OpenWorktree(ctx, path, label)
	}
	return m.Runtime.Open(ctx, path, label)
}

// checkTarget refuses locations that would cause trouble later.
func (m *Manager) checkTarget(path, repoPath string) error {
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", path)
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("%s already exists and is not empty", config.Contract(path))
		}
	}
	// A worktree nested inside another checkout means every indexer, file
	// watcher and ripgrep run in the outer repo sees a second copy of the
	// tree. dev keeps checkouts as siblings under the worktree root instead.
	if path == repoPath || strings.HasPrefix(path, repoPath+string(filepath.Separator)) {
		return fmt.Errorf("refusing to create a worktree inside the repository (%s); "+
			"set paths.worktree_path to a location outside %s",
			config.Contract(path), config.Contract(repoPath))
	}
	return nil
}

// RemoveRequest describes a worktree removal.
type RemoveRequest struct {
	RepoPath string
	Path     string
	// Force allows removing a checkout with uncommitted changes. dev never
	// sets this on its own: losing uncommitted work is exactly the outcome the
	// tool exists to prevent.
	Force bool
	// RuntimeHandle is closed before the checkout is removed, so the
	// multiplexer is not left pointing at a deleted directory.
	RuntimeHandle string
}

// Remove deletes a worktree checkout. It never deletes the branch — that stays
// a separate, explicit decision, matching git's own separation.
func (m *Manager) Remove(ctx context.Context, req RemoveRequest) error {
	if req.RuntimeHandle != "" && m.Runtime != nil {
		if err := m.Runtime.Close(ctx, req.RuntimeHandle); err != nil {
			m.logf("warning: could not close runtime session %s: %v", req.RuntimeHandle, err)
		}
	}
	if _, err := os.Stat(req.Path); errors.Is(err, os.ErrNotExist) {
		// The directory is already gone; clear git's administrative entry.
		return gitx.PruneWorktrees(ctx, req.RepoPath)
	}
	if err := gitx.RemoveWorktree(ctx, req.RepoPath, req.Path, req.Force); err != nil {
		return err
	}
	m.logf("removed worktree %s", config.Contract(req.Path))
	return nil
}

// DirtyCheck reports whether a checkout has uncommitted work, so callers can
// refuse to remove it without an explicit --force.
func DirtyCheck(ctx context.Context, path string) (bool, gitx.Status, error) {
	st, err := gitx.StatusOf(ctx, path)
	if err != nil {
		return false, st, err
	}
	return st.Dirty(), st, nil
}
