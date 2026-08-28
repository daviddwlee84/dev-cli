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

var errPromptCanceled = errors.New("prompt canceled")

type prompter struct {
	in    *bufio.Reader
	out   io.Writer
	style cliStyle
}

func newPrompter(app *App) *prompter {
	return &prompter{in: bufio.NewReader(app.In), out: app.Out, style: app.outStyle()}
}

func (p *prompter) line(label, fallback string) (string, error) {
	if fallback == "" {
		fmt.Fprintf(p.out, "%s %s: ", p.style.prompt("?"), p.style.prompt(label))
	} else {
		fmt.Fprintf(p.out, "%s %s %s: ", p.style.prompt("?"), p.style.prompt(label), p.style.dim("["+fallback+"]"))
	}
	line, err := p.in.ReadString('\n')
	if err != nil {
		return "", errPromptCanceled
	}
	value := strings.TrimSpace(line)
	if value == "" {
		value = fallback
	}
	return value, nil
}

func (p *prompter) dangerLine(label string) (string, error) {
	fmt.Fprintf(p.out, "%s %s: ", p.style.danger("?"), p.style.danger(label))
	line, err := p.in.ReadString('\n')
	if err != nil {
		return "", errPromptCanceled
	}
	return strings.TrimSpace(line), nil
}

func (p *prompter) choice(label, fallback, hint string, choices map[string]string) (string, error) {
	for {
		value, err := p.line(label, fallback)
		if err != nil {
			return "", err
		}
		value = strings.ToLower(value)
		if resolved, ok := choices[value]; ok {
			return resolved, nil
		}
		fmt.Fprintf(p.out, "  %s\n", p.style.warning("enter one of: "+hint))
	}
}

func (p *prompter) confirm(label string, defaultYes bool) (bool, error) {
	fallback := "y/N"
	if defaultYes {
		fallback = "Y/n"
	}
	for {
		fmt.Fprintf(p.out, "%s %s %s: ", p.style.prompt("?"), p.style.prompt(label), p.style.dim("["+fallback+"]"))
		line, err := p.in.ReadString('\n')
		if err != nil {
			return false, errPromptCanceled
		}
		value := strings.TrimSpace(line)
		switch strings.ToLower(value) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "":
			return defaultYes, nil
		default:
			fmt.Fprintln(p.out, "  "+p.style.warning("enter y or n"))
		}
	}
}

func promptStartRepository(ctx context.Context, app *App, p *prompter, req startRequest) (repo.Repo, error) {
	if req.RepoExplicit {
		return resolveStartRepository(ctx, app, req.RepoRef)
	}

	var current repo.Repo
	if r, err := resolveStartRepository(ctx, app, ""); err == nil {
		current = r
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
