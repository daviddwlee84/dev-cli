package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

var errStartCanceled = errors.New("start canceled")

type startPrompter struct {
	in  *bufio.Reader
	out io.Writer
}

func newStartPrompter(app *App) *startPrompter {
	return &startPrompter{in: bufio.NewReader(app.In), out: app.Out}
}

func (p *startPrompter) line(label, fallback string) (string, error) {
	if fallback == "" {
		fmt.Fprintf(p.out, "? %s: ", label)
	} else {
		fmt.Fprintf(p.out, "? %s [%s]: ", label, fallback)
	}
	line, err := p.in.ReadString('\n')
	if err != nil {
		return "", errStartCanceled
	}
	value := strings.TrimSpace(line)
	if value == "" {
		value = fallback
	}
	return value, nil
}

func (p *startPrompter) choice(label, fallback string, choices map[string]string) (string, error) {
	for {
		value, err := p.line(label, fallback)
		if err != nil {
			return "", err
		}
		value = strings.ToLower(value)
		if resolved, ok := choices[value]; ok {
			return resolved, nil
		}
		fmt.Fprintf(p.out, "  enter one of: worktree (w), branch-only (b), direct (d)\n")
	}
}

func (p *startPrompter) confirm() (bool, error) {
	for {
		value, err := p.line("Create this task?", "Y/n")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(value) {
		case "", "y", "yes", "y/n":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(p.out, "  enter y or n")
		}
	}
}

func promptStartRepository(ctx context.Context, app *App, p *startPrompter, req startRequest) (repo.Repo, error) {
	if req.RepoExplicit {
		return resolveStartRepository(ctx, app, req.RepoRef)
	}

	var current repo.Repo
	if r, err := resolveStartRepository(ctx, app, ""); err == nil {
		current = r
	}
	if current.Path == "" {
		all, err := repo.Discover(ctx, app.Cfg.ScanRoots(), repo.DefaultOptions())
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
		fmt.Fprintln(p.out, "Repositories:")
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
				fmt.Fprintln(p.out, "  repository is required")
				continue
			}
			var index int
			if _, err := fmt.Sscanf(value, "%d", &index); err == nil && index > 0 && index <= len(all) {
				return all[index-1], nil
			}
			r, _, err := repo.Resolve(ctx, app.Cfg.ScanRoots(), value)
			if err == nil {
				return r, nil
			}
			fmt.Fprintf(p.out, "  %v\n", err)
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
		r, _, err := repo.Resolve(ctx, app.Cfg.ScanRoots(), value)
		if err == nil {
			return r, nil
		}
		fmt.Fprintf(p.out, "  %v\n", err)
	}
}

func runStartWizard(ctx context.Context, app *App, req startRequest) (*startSpec, bool, error) {
	p := newStartPrompter(app)
	fmt.Fprintln(p.out, "Start a tracked change stream")

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
			fmt.Fprintln(p.out, "  task name is required")
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
			selected, err := p.choice("Mode (w=worktree, b=branch-only, d=direct)", fallback, map[string]string{
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
			fmt.Fprintf(p.out, "  %v\n", buildErr)
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
		fmt.Fprintln(p.out, "\nSummary")
		fmt.Fprintf(p.out, "  repository  %s (%s)\n", spec.RepoName, config.Contract(spec.RepoPath))
		fmt.Fprintf(p.out, "  task        %s\n", spec.Name)
		fmt.Fprintf(p.out, "  mode        %s\n", spec.Mode)
		fmt.Fprintf(p.out, "  branch      %s\n", spec.Branch)
		fmt.Fprintf(p.out, "  base        %s\n", spec.Base)
		fmt.Fprintf(p.out, "  checkout    %s\n", config.Contract(checkout))
		fmt.Fprintf(p.out, "  provision   %t\n", !spec.NoProvision)
		fmt.Fprintf(p.out, "  runtime     %s\n", rt.Name())
		fmt.Fprintf(p.out, "  focus       %t\n", spec.Focus)
		if spec.Next != "" {
			fmt.Fprintf(p.out, "  next        %s\n", spec.Next)
		}

		confirmed, err := p.confirm()
		if err != nil {
			return nil, false, err
		}
		return spec, confirmed, nil
	}
}
