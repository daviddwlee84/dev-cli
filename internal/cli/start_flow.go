package cli

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
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

type startRequest struct {
	RepoRef string
	Name    string
	Branch  string
	Base    string
	Next    string
	Run     string
	Mode    task.CheckoutMode

	RepoExplicit   bool
	ModeExplicit   bool
	BranchExplicit bool
	BaseExplicit   bool
	NextExplicit   bool

	NoProvision bool
	Focus       bool
}

type startSpec struct {
	RepoPath string
	RepoName string
	Category string
	Name     string
	Branch   string
	Base     string
	Next     string
	Run      string
	Mode     task.CheckoutMode

	WorktreePath string
	NoProvision  bool
	Focus        bool
}

type startResult struct {
	Task     *task.Task
	Runtime  runtime.Runtime
	Opened   runtime.OpenResult
	Worktree *wt.CreateResult
}

func resolveStartRepository(ctx context.Context, app *App, ref string) (repo.Repo, error) {
	if ref != "" {
		r, _, err := repo.Resolve(ctx, app.Cfg.DiscoveryRoots(), ref)
		return r, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return repo.Repo{}, err
	}
	g, err := gitx.Discover(ctx, cwd)
	if err != nil {
		return repo.Repo{}, fmt.Errorf("no repo argument and %s is not a git repository", config.Contract(cwd))
	}
	return repo.Repo{
		Name: g.Name, Path: g.MainRoot, RealPath: g.MainRoot,
		MainRoot: g.MainRoot, CommonDir: g.GitCommonDir, HasGit: true, Bare: g.Bare,
	}, nil
}

func buildStartSpec(ctx context.Context, app *App, req startRequest) (*startSpec, error) {
	r, err := resolveStartRepository(ctx, app, req.RepoRef)
	if err != nil {
		return nil, err
	}
	return buildStartSpecForRepository(ctx, app, r, req)
}

func buildStartSpecForRepository(ctx context.Context, app *App, r repo.Repo, req startRequest) (*startSpec, error) {
	if r.Bare {
		return nil, fmt.Errorf("%s is a bare repository and cannot host a task checkout", r.Display())
	}

	name := strings.TrimSpace(req.Name)
	if name == "" && req.Branch != "" && req.Mode != task.ModeDirect {
		name = req.Branch
	}
	if name == "" {
		return nil, errors.New("give the work a name: dev start <repo> --task <name>")
	}

	branch, base := strings.TrimSpace(req.Branch), strings.TrimSpace(req.Base)
	mode := req.Mode
	if mode == "" {
		mode = task.ModeWorktree
	}
	switch mode {
	case task.ModeDirect:
		st, err := gitx.StatusOf(ctx, r.Path)
		if err != nil {
			return nil, err
		}
		if st.Detached || st.Branch == "" {
			return nil, errors.New("--direct needs a named branch; this checkout has detached HEAD")
		}
		if branch != "" && branch != st.Branch {
			return nil, fmt.Errorf("--direct tracks the branch already checked out (%s), not --branch %s; "+
				"omit --branch or use --branch-only", st.Branch, branch)
		}
		branch, base = st.Branch, st.Branch

	case task.ModeBranch, task.ModeWorktree:
		if branch == "" {
			branch = "feat/" + config.Slug(name)
		}
		if base == "" {
			base = gitx.DefaultBranch(ctx, r.Path)
		}
		if _, err := gitx.Run(ctx, r.Path, "check-ref-format", "--branch", branch); err != nil {
			return nil, fmt.Errorf("invalid branch %q: %w", branch, err)
		}
		if !gitx.BranchExists(ctx, r.Path, branch) {
			if base == "" {
				return nil, errors.New("cannot determine a base ref; enter one explicitly")
			}
			if !gitx.RefExists(ctx, r.Path, base) {
				return nil, fmt.Errorf("base ref %q does not exist in %s", base, r.Path)
			}
		}

	default:
		return nil, fmt.Errorf("unknown start mode %q", mode)
	}
	if req.Run != "" && mode != task.ModeWorktree {
		return nil, errors.New("--run requires the default worktree mode; it cannot be used with --direct or --branch-only")
	}

	id := task.MakeID(r.Name, branch)
	existing, err := existingTaskForStart(app.Tasks, id)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.State != task.Done {
		return nil, fmt.Errorf("task %s already exists (state %s) — use `dev resume %s`",
			existing.ID, existing.State, existing.ID)
	}

	spec := &startSpec{
		RepoPath: r.Path, RepoName: r.Name, Category: r.Category,
		Name: name, Branch: branch, Base: base, Next: req.Next, Run: req.Run, Mode: mode,
		NoProvision: req.NoProvision, Focus: req.Focus,
	}
	if mode == task.ModeWorktree {
		if existing, ok, err := gitx.WorktreeFor(ctx, r.Path, branch); err != nil {
			return nil, err
		} else if ok {
			return nil, &wt.ErrExists{Branch: branch, Path: existing.Path}
		}
		path, err := app.Cfg.WorktreePathFor(r.Name, r.Path, branch, r.Category)
		if err != nil {
			return nil, err
		}
		if err := wt.ValidateTarget(path, r.Path); err != nil {
			return nil, err
		}
		spec.WorktreePath = path
	}
	return spec, nil
}

func existingTaskForStart(store *task.Store, id string) (*task.Task, error) {
	existing, err := store.Get(id)
	if err == nil {
		return existing, nil
	}
	if errors.Is(err, task.ErrNotFound) {
		return nil, nil
	}
	if present, presentErr := store.RecordExists(id); presentErr == nil && present {
		return nil, err
	} else if presentErr != nil {
		// Preserve the established late-save failure behavior for a task-store
		// path whose existing ancestor is itself not a directory. Any usable store
		// with an occupied corrupt record fails before external side effects.
		if taskStoreBlockedByNonDirectory(store.Dir) {
			return nil, nil
		}
		return nil, err
	}
	return nil, nil
}

func taskStoreBlockedByNonDirectory(path string) bool {
	for {
		info, err := os.Lstat(path)
		if err == nil {
			return !info.IsDir()
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
}

func executeStartSpec(ctx context.Context, app *App, spec *startSpec, log io.Writer) (*startResult, error) {
	id := task.MakeID(spec.RepoName, spec.Branch)
	replaceDoneRevision := ""
	existing, err := existingTaskForStart(app.Tasks, id)
	if err != nil {
		return nil, err
	}
	switch {
	case existing != nil && existing.State != task.Done:
		return nil, fmt.Errorf("task %s already exists (state %s) — use `dev resume %s`",
			existing.ID, existing.State, existing.ID)
	case existing != nil:
		replaceDoneRevision = existing.Revision()
	}

	t := &task.Task{
		Name: spec.Name, Repo: spec.RepoName, RepoPath: spec.RepoPath,
		Branch: spec.Branch, Base: spec.Base, Mode: spec.Mode,
		State: task.Hot, Owner: config.Hostname(), Next: spec.Next,
	}
	rt := app.Runtime()
	label := worktreeRuntimeLabel(spec.RepoName, spec.Branch)
	if spec.Mode == task.ModeDirect {
		label = spec.RepoName + "/" + spec.Name
	}
	result := &startResult{Task: t, Runtime: rt}

	switch spec.Mode {
	case task.ModeDirect:
		if err := guardSharedCheckout(ctx, app, rt, spec.RepoPath); err != nil {
			return nil, err
		}
		opened, err := rt.Open(ctx, spec.RepoPath, label)
		if err != nil {
			app.warnf("could not open a runtime session: %v", err)
		} else {
			result.Opened = opened
		}

	case task.ModeBranch:
		if err := guardSharedCheckout(ctx, app, rt, spec.RepoPath); err != nil {
			return nil, err
		}
		if gitx.BranchExists(ctx, spec.RepoPath, spec.Branch) {
			if _, err := gitx.Run(ctx, spec.RepoPath, "switch", spec.Branch); err != nil {
				return nil, fmt.Errorf("switch to %s: %w", spec.Branch, err)
			}
		} else if _, err := gitx.Run(ctx, spec.RepoPath, "switch", "-c", spec.Branch, spec.Base); err != nil {
			return nil, fmt.Errorf("create and switch to %s from %s: %w", spec.Branch, spec.Base, err)
		}
		opened, err := rt.Open(ctx, spec.RepoPath, label)
		if err != nil {
			app.warnf("could not open a runtime session: %v", err)
		} else {
			result.Opened = opened
		}

	case task.ModeWorktree:
		m := &wt.Manager{Cfg: app.Cfg, Runtime: rt, Log: log}
		created, err := m.Create(ctx, wt.CreateRequest{
			RepoPath: spec.RepoPath, RepoName: spec.RepoName,
			Branch: spec.Branch, Base: spec.Base, Category: spec.Category,
			Path: spec.WorktreePath, Label: label,
			NoProvision: spec.NoProvision, Focus: spec.Focus,
		})
		if err != nil {
			return nil, err
		}
		t.WorktreePath, result.Opened, result.Worktree = created.Path, created.Runtime, created
	}

	setTaskRuntime(t, rt, result.Opened)
	if replaceDoneRevision != "" {
		err = app.Tasks.ReplaceDone(t, replaceDoneRevision)
	} else {
		err = app.Tasks.Save(t)
	}
	if err != nil {
		return nil, err
	}
	annotate(app, rt, t)
	return result, nil
}
