package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/submodule"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/spf13/cobra"
)

func parseSubmoduleBases(values []string) (map[string]string, error) {
	out := map[string]string{}
	for _, v := range values {
		p, ref, ok := strings.Cut(v, "=")
		if !ok || ref == "" {
			return nil, errors.New("--submodule-base requires PATH=REF")
		}
		if err := (config.Submodules{Develop: []string{p}}).Validate(); err != nil {
			return nil, err
		}
		if _, ok := out[p]; ok {
			return nil, fmt.Errorf("duplicate submodule base %s", p)
		}
		out[p] = ref
	}
	return out, nil
}

func newSubmoduleCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "submodule", Short: "Inspect, initialize and develop the submodules of a workspace"}
	var jsonOut bool
	status := &cobra.Command{Use: "status", Short: "Read the recursive gitlink and checkout graph without network access", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		g, err := gitx.SubmodulesOf(ctxOf(), cwd)
		if err != nil {
			return err
		}
		convention, worktrees, observeErr := submodule.ChildWorktrees(ctxOf(), g)
		if jsonOut {
			out := submoduleStatusJSON{SubmoduleGraph: g, ChildWorktrees: worktrees}
			if convention.Detected {
				out.Convention = &convention
			}
			if err := json.NewEncoder(app.Out).Encode(out); err != nil {
				return err
			}
			return observeErr
		}
		renderSubmoduleConvention(app, g.Root, convention)
		renderSubmodulesWithWorktrees(app, g.Nodes, worktrees)
		if observeErr != nil {
			return fmt.Errorf("child worktree observation incomplete: %w", observeErr)
		}
		return nil
	}}
	status.Flags().BoolVar(&jsonOut, "json", false, "emit the submodule graph as JSON")
	var dryRun bool
	init := &cobra.Command{Use: "init", Short: "Initialize missing submodules at their gitlinks; preserve existing checkouts", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := guardSharedCheckout(ctxOf(), app, app.Runtime(), cwd); err != nil {
			return err
		}
		g, err := gitx.SubmodulesOf(ctxOf(), cwd)
		if err != nil {
			return err
		}
		if !dryRun {
			r, discoverErr := gitx.Discover(ctxOf(), cwd)
			if discoverErr != nil {
				return discoverErr
			}
			err = gitx.WithLifecycleLock(ctxOf(), r.GitCommonDir, func() error { var err error; g, err = gitx.InitSubmodules(ctxOf(), cwd); return err })
		}
		renderSubmodules(app, g.Nodes)
		return err
	}}
	init.Flags().BoolVar(&dryRun, "dry-run", false, "inspect without initializing or contacting remotes")
	var bases []string
	develop := &cobra.Command{Use: "develop <path>...", Short: "Add selected submodules to the current managed task branch", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		r, err := gitx.Discover(ctxOf(), cwd)
		if err != nil {
			return err
		}
		t, err := exactTaskForRetirementCheckout(app, r.Root)
		if err != nil {
			return err
		}
		if t == nil || t.EffectiveMode() != task.ModeWorktree || (t.State != task.Hot && t.State != task.Warm) {
			return errors.New("develop requires a HOT/WARM managed worktree task")
		}
		if err := guardSharedCheckout(ctxOf(), app, app.Runtime(), r.Root); err != nil {
			return err
		}
		parsed, err := parseSubmoduleBases(bases)
		if err != nil {
			return err
		}
		var g gitx.SubmoduleGraph
		err = gitx.WithLifecycleLock(ctxOf(), r.GitCommonDir, func() error {
			var err error
			g, err = submodule.Prepare(ctxOf(), app.Cfg, r.Root, "recursive", args, parsed, false)
			return err
		})
		renderSubmodules(app, g.Nodes)
		return err
	}}
	develop.Flags().StringArrayVar(&bases, "submodule-base", nil, "submodule integration target PATH=REF (repeatable)")
	var recoveryDryRun bool
	recover := &cobra.Command{Use: "recover <journal.json>", Short: "Restore child repositories retained after interrupted recursive cleanup", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return submodule.Recover(ctxOf(), args[0], func(ctx context.Context, root string) error {
			return guardNewAgentCheckout(ctx, app, app.Runtime(), root)
		}, recoveryDryRun)
	}}
	recover.Flags().BoolVar(&recoveryDryRun, "dry-run", false, "validate the recovery journal without changing files")
	cmd.AddCommand(status, init, develop, recover, newSubmoduleAddCmd(app, false))
	return cmd
}

// submoduleStatusJSON adds fields to the graph object; existing keys are kept.
type submoduleStatusJSON struct {
	gitx.SubmoduleGraph
	Convention     *submodule.Convention     `json:"convention,omitempty"`
	ChildWorktrees []submodule.ChildWorktree `json:"child_worktrees,omitempty"`
}

func renderSubmoduleConvention(app *App, root string, c submodule.Convention) {
	if !c.Detected {
		return
	}
	where := c.WorktreeRoot
	if rel, err := filepath.Rel(root, c.WorktreeRoot); err == nil && c.WorktreeRootInside {
		where = filepath.ToSlash(rel)
	}
	fmt.Fprintf(app.Out, "%s convention (%s); child worktree root %s [%s]\n", submodule.ConventionName, strings.Join(c.Sources, ", "), where, c.WorktreeRootSource)
	if c.WorktreeRootInside {
		fmt.Fprintln(app.Out, "  root is inside the repository; `git config --local superproject.worktreeRoot <dir outside the repo>` keeps child worktrees outside it")
	}
}

func renderSubmodules(app *App, nodes []gitx.SubmoduleNode) {
	renderSubmodulesWithWorktrees(app, nodes, nil)
}

func renderSubmodulesWithWorktrees(app *App, nodes []gitx.SubmoduleNode, worktrees []submodule.ChildWorktree) {
	depths := map[string]int{"": -1}
	for _, n := range nodes {
		depth := depths[n.Parent] + 1
		depths[n.Path] = depth
		state := n.State
		if n.Initialized {
			branch := n.Branch
			if n.Status.Detached {
				branch = "pinned"
			}
			state = branch + " @ " + n.HEAD[:min(12, len(n.HEAD))]
		}
		if n.SkipsInitialization() && n.State == "uninitialized" {
			state += " (update = none: initialized only when developed)"
		}
		fmt.Fprintf(app.Out, "%s  submodule %-28s %s", strings.Repeat("  ", depth), n.Path, state)
		if len(n.Blockers) > 0 {
			fmt.Fprintf(app.Out, " · %s", strings.Join(n.Blockers, "; "))
		}
		fmt.Fprintln(app.Out)
		for _, w := range worktrees {
			if w.Submodule != n.Path {
				continue
			}
			branch := w.Branch
			if branch == "" {
				branch = "detached"
			}
			fmt.Fprintf(app.Out, "%s    worktree %s [%s]\n", strings.Repeat("  ", depth), w.Label(), branch)
		}
	}
}
