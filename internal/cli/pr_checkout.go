package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/prflow"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/spf13/cobra"
)

type prCheckoutFlags struct {
	// RequiredCheckout pins a TUI provision action to the exact selected path.
	RequiredCheckout                            string
	Repo, Destination, SourceRemote             string
	Clone, Try, Provision, NoOpen, DryRun, JSON bool
}

func newPRCheckoutCmd(app *App) *cobra.Command {
	opts := prCheckoutFlags{}
	cmd := &cobra.Command{Use: "checkout <URL>", Short: "Open a request in an existing checkout, a task-free worktree, or a Try", Long: `Resolve the exact request head and reuse a matching local checkout without resetting it.
Otherwise create a worktree from the verified head. No task or fork is created.

Without a local repository, use --try for an independent dated clone or --clone
for a project clone plus worktree. The interactive choice defaults to Try.
Provisioning and submodule initialization are opt-in with --provision.
JSON mode returns paths and retained effects without opening a runtime.`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ref, err := prflow.ParseReference(args[0])
		if err != nil {
			return err
		}
		_, err = runPRCheckout(cmd.Context(), app, ref, opts)
		return err
	}}
	f := cmd.Flags()
	f.StringVar(&opts.Repo, "repo", "", "select an exact local repository or checkout")
	f.StringVar(&opts.SourceRemote, "source-remote", "", "select the existing base repository remote used to fetch the PR")
	f.BoolVar(&opts.Clone, "clone", false, "clone the base repository into project_root before creating a worktree")
	f.BoolVar(&opts.Try, "try", false, "create an independent PR clone under tries_root")
	f.StringVar(&opts.Destination, "path", "", "project clone destination (requires --clone)")
	f.BoolVar(&opts.Provision, "provision", false, "explicitly provision the new checkout and initialize configured submodules")
	f.BoolVar(&opts.NoOpen, "no-open", false, "prepare the checkout without opening a runtime or changing directory")
	f.BoolVar(&opts.DryRun, "dry-run", false, "inspect the checkout plan without fetching or changing files")
	f.BoolVar(&opts.JSON, "json", false, "emit a plan or result with retained effects; do not open a runtime")
	return cmd
}

func prLocalRepositories(ctx context.Context, app *App, explicit string) ([]string, error) {
	if explicit != "" {
		resolved, _, err := resolveRepoRef(app, explicit)
		if err != nil {
			return nil, err
		}
		return []string{resolved.Path}, nil
	}
	paths := map[string]bool{}
	if cwd, err := os.Getwd(); err == nil {
		if local, e := gitx.Discover(ctx, cwd); e == nil {
			paths[local.MainRoot] = true
		}
	}
	locals, err := repo.Discover(ctx, app.Cfg.DiscoveryRoots(), repo.DefaultOptions())
	if err != nil {
		return nil, err
	}
	for _, local := range locals {
		if local.HasGit {
			paths[local.Path] = true
		}
	}
	var result []string
	for p := range paths {
		result = append(result, p)
	}
	return result, nil
}

func prCheckoutService(ctx context.Context, app *App, ref prflow.Reference, explicit string) (*prflow.CheckoutService, error) {
	return prCheckoutServiceFor(ctx, app, ref, explicit, true)
}

func prCheckoutServiceFor(ctx context.Context, app *App, ref prflow.Reference, explicit string, discover bool) (*prflow.CheckoutService, error) {
	var paths []string
	var err error
	if discover {
		paths, err = prLocalRepositories(ctx, app, explicit)
		if err != nil {
			return nil, err
		}
	}
	var experiments *experiment.Service
	if app.Catalog != nil && app.Registry != nil {
		experiments, err = newExperimentService(app)
		if err != nil {
			return nil, err
		}
	}
	return prflow.NewCheckoutService(prflow.CheckoutConfig{Config: app.Cfg, Tasks: app.Tasks, Experiments: experiments, Resolver: prService(app, ref), Repositories: paths, Log: app.Err,
		ProvisionGuard: func(ctx context.Context, path string) error {
			backends := prSyncRuntimes(app)
			if len(backends) == 0 {
				return errors.New("provisioning requires runtime occupancy observations; runtime observation is disabled")
			}
			for _, backend := range backends {
				if err := guardSharedCheckout(ctx, app, backend, path); err != nil {
					return err
				}
			}
			return nil
		},
	}), nil
}

func runPRCheckout(ctx context.Context, app *App, ref prflow.Reference, opts prCheckoutFlags) (prflow.CheckoutResult, error) {
	var empty prflow.CheckoutResult
	if opts.Clone && opts.Try || opts.Repo != "" && (opts.Clone || opts.Try) {
		return empty, errors.New("select one of --repo, --clone or --try")
	}
	if opts.Destination != "" && !opts.Clone {
		return empty, errors.New("--path requires --clone")
	}
	if opts.SourceRemote != "" && (opts.Clone || opts.Try) {
		return empty, errors.New("--source-remote selects an existing repository remote and cannot be combined with --clone or --try")
	}
	service, err := prCheckoutServiceFor(ctx, app, ref, opts.Repo, !opts.Clone && !opts.Try)
	if err != nil {
		return empty, err
	}
	req := prflow.CheckoutRequest{Reference: ref, Try: opts.Try, Provision: opts.Provision, SourceRemote: opts.SourceRemote}
	if opts.Repo != "" {
		local, _, err := resolveRepoRef(app, opts.Repo)
		if err != nil {
			return empty, err
		}
		req.RepoPath = local.Path
	}
	setClone := func() error {
		destination := opts.Destination
		if destination == "" {
			var err error
			destination, err = tuiRemoteCloneDestination(app, path.Base(ref.Repo))
			if err != nil {
				return err
			}
		}
		absolute, err := filepath.Abs(config.Expand(destination))
		if err != nil {
			return err
		}
		if app.workflowHandoff != nil {
			discoverable, err := repositoryPathDiscoverable(app.Cfg, absolute)
			if err != nil {
				return err
			}
			if !discoverable {
				return fmt.Errorf("%s is outside REPOS discovery; add project_root to scan_roots before cloning from REMOTE", config.Contract(absolute))
			}
		}
		req.CloneDestination = absolute
		return nil
	}
	if opts.Clone {
		if err = setClone(); err != nil {
			return empty, err
		}
	}
	var plan prflow.CheckoutPlan
	for {
		plan, err = service.Plan(ctx, req)
		if err == nil {
			if opts.RequiredCheckout != "" && (!plan.Reused || !samePRLocalPath(plan.Path, opts.RequiredCheckout)) {
				return empty, errors.New("selected PR checkout changed; inspect it before provisioning")
			}
			break
		}
		if !app.interactive() || opts.JSON || opts.DryRun || opts.RequiredCheckout != "" {
			return empty, err
		}
		if errors.Is(err, prflow.ErrNoLocalRepository) {
			choice, e := newPrompter(app).choiceOf("No local repository — prepare", "try", []string{"try", "clone", "cancel"}, map[string]string{"try": "try", "clone": "clone", "cancel": "cancel"})
			if e != nil {
				return empty, e
			}
			switch choice {
			case "try":
				req.Try = true
			case "clone":
				if e = setClone(); e != nil {
					return empty, e
				}
			default:
				return empty, errPromptCanceled
			}
			continue
		}
		var ambiguous *prflow.AmbiguousCheckoutError
		if errors.As(err, &ambiguous) {
			for i, c := range ambiguous.Candidates {
				selected := c.Path
				if selected == "" {
					selected = c.RepoPath
				}
				fmt.Fprintf(app.Out, "  %d  %s\n", i+1, config.Contract(selected))
			}
			answer, e := newPrompter(app).line("Repository number (or cancel)", "cancel")
			if e != nil {
				return empty, e
			}
			if answer == "cancel" {
				return empty, errPromptCanceled
			}
			n, e := strconv.Atoi(answer)
			if e != nil || n < 1 || n > len(ambiguous.Candidates) {
				return empty, errors.New("choose a listed repository number")
			}
			selected := ambiguous.Candidates[n-1]
			req.RepoPath = selected.Path
			if req.RepoPath == "" {
				req.RepoPath = selected.RepoPath
			}
			continue
		}
		return empty, err
	}
	if opts.DryRun {
		if opts.JSON {
			return empty, prWriteJSON(app.Out, plan)
		}
		renderPRCheckoutPlan(app, plan)
		return empty, nil
	}
	if !opts.JSON {
		renderPRCheckoutPlan(app, plan)
	}
	result, applyErr := service.Apply(ctx, plan)
	if opts.JSON {
		writeErr := prWriteJSON(app.Out, struct {
			prflow.CheckoutResult
			Error string `json:"error,omitempty"`
		}{result, prErrorText(applyErr)})
		return result, errors.Join(applyErr, writeErr)
	}
	for _, effect := range result.Effects {
		fmt.Fprintf(app.Out, "  %s: %s", effect.Stage, effect.Status)
		if effect.Path != "" {
			fmt.Fprintf(app.Out, " · %s", config.Contract(effect.Path))
		}
		if effect.Detail != "" {
			fmt.Fprintf(app.Out, " · %s", terminalText(effect.Detail))
		}
		fmt.Fprintln(app.Out)
	}
	for _, warning := range result.Warnings {
		app.warnf("%s", warning)
	}
	if applyErr != nil {
		return result, applyErr
	}
	if result.Differs {
		fmt.Fprintln(app.Out, "Existing checkout differs from the latest PR head; local contents were retained.")
	}
	if !opts.NoOpen {
		rt := app.Runtime()
		opened, err := openCheckout(ctx, rt, result.Path, result.Label)
		if err != nil {
			return result, fmt.Errorf("checkout retained at %s; runtime open failed: %w", result.Path, err)
		}
		if rt.Name() == "none" {
			return result, app.cdDirective(result.Path)
		}
		if err = app.activate(ctx, rt, opened.Handle); err != nil {
			return result, fmt.Errorf("checkout retained at %s; runtime focus failed: %w", result.Path, err)
		}
	}
	return result, nil
}

func renderPRCheckoutPlan(app *App, p prflow.CheckoutPlan) {
	mode := "create worktree"
	if p.Reused {
		mode = "open existing checkout"
	} else if p.Try {
		mode = "create Try clone"
	} else if p.Clone {
		mode = "clone project and create worktree"
	}
	fmt.Fprintf(app.Out, "%s\n  request  %s\n  branch   %s\n  head     %s\n  path     %s\n", mode, p.Detail.Reference.URL(), terminalText(p.Branch), p.HeadOID, config.Contract(p.Path))
	if p.Request.Provision {
		fmt.Fprintln(app.Out, "  provisioning explicitly selected")
	}
}

func prErrorText(err error) string {
	if err == nil {
		return ""
	}
	return terminalText(err.Error())
}

func (w *prTUIWorkflow) provision(ref prflow.Reference) error {
	if w.request.Row.LocalPath == "" {
		return errors.New("PR checkout changed; refresh or open the checkout first")
	}
	w.result.RefreshRepos = true
	_, err := runPRCheckout(w.ctx, &w.app, ref, prCheckoutFlags{Repo: w.request.Row.LocalPath, RequiredCheckout: w.request.Row.LocalPath, Provision: true, NoOpen: true})
	return err
}
