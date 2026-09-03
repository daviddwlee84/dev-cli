package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func promptStartRepository(ctx context.Context, app *App, p *prompter, req startRequest) (repo.Repo, error) {
	if req.RepoExplicit {
		return resolveStartRepository(ctx, app, req.RepoRef)
	}

	var current repo.Repo
	if r, err := resolveStartRepository(ctx, app, ""); err == nil {
		current = r
	}
	if current.Path == "" && app.canPick() {
		return pickStartRepository(ctx, app, p)
	}
	if current.Path == "" {
		all, err := repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
		if err != nil {
			return repo.Repo{}, err
		}
		candidates := all[:0:0]
		for _, r := range all {
			if r.HasGit && !r.Bare {
				candidates = append(candidates, r)
			}
		}
		all = candidates
		if len(all) == 0 {
			return repo.Repo{}, errors.New("no non-bare repositories found under paths.scan_roots")
		}
		fmt.Fprintln(p.out, p.style.title("Repositories:"))
		for i, r := range all {
			fmt.Fprintf(p.out, "  %d) %-24s %s\n", i+1, r.Display(), config.Contract(r.Path))
		}
		fallback := ""
		if len(all) == 1 {
			fallback = all[0].Display()
		}
		for {
			value, err := p.line("Repository", fallback)
			if err != nil {
				return repo.Repo{}, err
			}
			if value == "" {
				fmt.Fprintln(p.out, "  "+p.style.warning("repository is required"))
				continue
			}
			var index int
			if _, err := fmt.Sscanf(value, "%d", &index); err == nil && index > 0 && index <= len(all) {
				return all[index-1], nil
			}
			r, _, err := repo.Resolve(ctx, app.Cfg.DiscoveryRoots(), value)
			if err == nil {
				return r, nil
			}
			fmt.Fprintf(p.out, "  %s\n", p.style.warning(err.Error()))
		}
	}

	for {
		value, err := p.line("Repository", current.Display())
		if err != nil {
			return repo.Repo{}, err
		}
		if value == current.Display() || value == current.Name || value == current.Path {
			return current, nil
		}
		r, _, err := repo.Resolve(ctx, app.Cfg.DiscoveryRoots(), value)
		if err == nil {
			return r, nil
		}
		fmt.Fprintf(p.out, "  %s\n", p.style.warning(err.Error()))
	}
}

const manualStartRepository = "\x00manual-start-repository"

func pickStartRepository(ctx context.Context, app *App, p *prompter) (repo.Repo, error) {
	for {
		discovered, err := repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.CompletionOptions())
		if err != nil {
			return repo.Repo{}, err
		}

		items := make([]picker.Item, 0, len(discovered)+1)
		for _, repository := range discovered {
			if !repository.HasGit || repository.Bare {
				continue
			}
			items = append(items, picker.Item{
				Value:       repository.Path,
				Label:       repository.Display(),
				Description: config.Contract(repository.Path),
			})
		}
		if len(items) == 0 {
			return repo.Repo{}, errors.New("no non-bare repositories found under paths.scan_roots")
		}
		items = append(items, picker.Item{
			Value:       manualStartRepository,
			Label:       "Enter a repository name or path manually…",
			Description: "resolve a repository not present in the discovered list",
		})

		result, used, err := app.pick(ctx, picker.Request{Prompt: "Repository", Items: items})
		if errors.Is(err, picker.ErrCanceled) {
			return repo.Repo{}, errPromptCanceled
		}
		if err != nil {
			return repo.Repo{}, err
		}
		if !used || result.Item.Value == manualStartRepository {
			return promptManualStartRepository(ctx, app, p)
		}
		if result.Item.Value == "" {
			return repo.Repo{}, errors.New("picker returned an empty repository path")
		}
		resolved, err := resolveStartRepository(ctx, app, result.Item.Value)
		if err == nil {
			return resolved, nil
		}
		fmt.Fprintf(p.out, "  %s\n", p.style.warning("selected repository changed or disappeared; choose again"))
	}
}

func promptManualStartRepository(ctx context.Context, app *App, p *prompter) (repo.Repo, error) {
	for {
		value, err := p.line("Repository", "")
		if err != nil {
			return repo.Repo{}, err
		}
		if value == "" {
			fmt.Fprintln(p.out, "  "+p.style.warning("repository is required"))
			continue
		}
		resolved, _, err := repo.Resolve(ctx, app.Cfg.DiscoveryRoots(), value)
		if err == nil {
			return resolved, nil
		}
		fmt.Fprintf(p.out, "  %s\n", p.style.warning(err.Error()))
	}
}

func runStartWizard(ctx context.Context, app *App, req startRequest) (*startSpec, bool, error) {
	p := newPrompter(app)
	fmt.Fprintln(p.out, p.style.title("Start a tracked change stream"))

	r, err := promptStartRepository(ctx, app, p, req)
	if err != nil {
		return nil, false, err
	}
	if r.Bare {
		return nil, false, fmt.Errorf("%s is a bare repository and cannot host a task checkout", r.Display())
	}
	req.RepoRef, req.RepoExplicit = r.Path, true

	for strings.TrimSpace(req.Name) == "" {
		req.Name, err = p.line("Task name", "")
		if err != nil {
			return nil, false, err
		}
		if strings.TrimSpace(req.Name) == "" {
			fmt.Fprintln(p.out, "  "+p.style.warning("task name is required"))
		}
	}

	for {
		if !req.ModeExplicit {
			fallback := "worktree"
			switch req.Mode {
			case task.ModeBranch:
				fallback = "branch-only"
			case task.ModeDirect:
				fallback = "direct"
			}
			selected, err := p.choice("Mode (w=worktree, b=branch-only, d=direct)", fallback,
				"worktree (w), branch-only (b), direct (d)", map[string]string{
					"w": "worktree", "worktree": "worktree",
					"b": "branch-only", "branch": "branch-only", "branch-only": "branch-only",
					"d": "direct", "direct": "direct",
				})
			if err != nil {
				return nil, false, err
			}
			switch selected {
			case "direct":
				req.Mode = task.ModeDirect
			case "branch-only":
				req.Mode = task.ModeBranch
			default:
				req.Mode = task.ModeWorktree
			}
		}

		if req.Mode == task.ModeDirect {
			st, statusErr := gitx.StatusOf(ctx, r.Path)
			if statusErr != nil {
				return nil, false, statusErr
			}
			req.Branch, req.Base = st.Branch, st.Branch
		} else {
			if !req.BranchExplicit {
				fallback := req.Branch
				if fallback == "" {
					fallback = "feat/" + config.Slug(req.Name)
				}
				req.Branch, err = p.line("Branch", fallback)
				if err != nil {
					return nil, false, err
				}
			}
			if !req.BaseExplicit {
				fallback := req.Base
				if fallback == "" {
					fallback = gitx.DefaultBranch(ctx, r.Path)
				}
				req.Base, err = p.line("Base", fallback)
				if err != nil {
					return nil, false, err
				}
			}
		}
		if !req.NextExplicit {
			req.Next, err = p.line("Next action (optional)", req.Next)
			if err != nil {
				return nil, false, err
			}
		}

		spec, buildErr := buildStartSpec(ctx, app, req)
		if buildErr != nil {
			fmt.Fprintf(p.out, "  %s\n", p.style.warning(buildErr.Error()))
			if req.BranchExplicit || req.BaseExplicit || req.ModeExplicit {
				return nil, false, buildErr
			}
			req.Branch, req.Base = "", ""
			continue
		}

		rt := app.Runtime()
		checkout := spec.RepoPath
		if spec.WorktreePath != "" {
			checkout = spec.WorktreePath
		}
		fmt.Fprintln(p.out, "\n"+p.style.title("Summary"))
		fmt.Fprintf(p.out, "  %s  %s (%s)\n", p.style.label("repository"), spec.RepoName, config.Contract(spec.RepoPath))
		fmt.Fprintf(p.out, "  %s        %s\n", p.style.label("task"), spec.Name)
		fmt.Fprintf(p.out, "  %s        %s\n", p.style.label("mode"), spec.Mode)
		fmt.Fprintf(p.out, "  %s      %s\n", p.style.label("branch"), spec.Branch)
		fmt.Fprintf(p.out, "  %s        %s\n", p.style.label("base"), spec.Base)
		fmt.Fprintf(p.out, "  %s    %s\n", p.style.label("checkout"), config.Contract(checkout))
		fmt.Fprintf(p.out, "  %s   %s\n", p.style.label("provision"), p.style.success(fmt.Sprint(!spec.NoProvision)))
		fmt.Fprintf(p.out, "  %s     %s\n", p.style.label("runtime"), p.style.success(rt.Name()))
		fmt.Fprintf(p.out, "  %s       %s\n", p.style.label("focus"), fmt.Sprint(spec.Focus))
		if spec.Run != "" {
			fmt.Fprintf(p.out, "  %s         %s\n", p.style.label("run"), "configured for the new Herdr root pane")
		}
		if spec.Next != "" {
			fmt.Fprintf(p.out, "  %s        %s\n", p.style.label("next"), spec.Next)
		}

		confirmed, err := p.confirm("Create this task?", true)
		if err != nil {
			return nil, false, err
		}
		return spec, confirmed, nil
	}
}
