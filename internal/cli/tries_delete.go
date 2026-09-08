package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

var errRemovalRuntimeUnknown = errors.New("runtime coverage is unknown; --assume-no-runtime requires explicitly confirming that the Try is unused")

type tryDeleteOptions struct {
	permanent, dryRun, json, yes, assumeNoRuntime bool
	confirmDelete                                 string
	expected                                      *catalog.Entry
}

func newTriesDeleteCmd(app *App) *cobra.Command {
	var opts tryDeleteOptions
	cmd := &cobra.Command{Use: "delete <ref>", Aliases: []string{"rm"}, Short: "Move a Try to system Trash, or explicitly discard it permanently", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return runTryDelete(cmd.Context(), app, args[0], opts)
	}}
	f := cmd.Flags()
	f.BoolVar(&opts.permanent, "permanent", false, "permanently discard all Try contents instead of using Trash")
	f.BoolVar(&opts.dryRun, "dry-run", false, "preview without removing anything")
	f.BoolVar(&opts.json, "json", false, "emit a structured preview/result without prompting")
	f.BoolVar(&opts.yes, "yes", false, "approve moving the selected Try to Trash")
	f.StringVar(&opts.confirmDelete, "confirm-delete", "", "confirm permanent data loss for this exact catalog ID")
	f.BoolVar(&opts.assumeNoRuntime, "assume-no-runtime", false, "acknowledge unobserved runtime coverage; never overrides observed occupation")
	return cmd
}

func newRemovalService(app *App, assume bool) (*experiment.Service, error) {
	return experiment.NewService(experiment.ServiceConfig{
		Registry: app.Registry, Store: app.Catalog, TriesRoot: config.Expand(app.Cfg.Paths.TriesRoot), ProjectRoot: config.Expand(app.Cfg.Paths.ProjectRoot), Host: config.Hostname(),
		Hooks: experiment.Hooks{RemovalGuard: func(ctx context.Context, path string) (string, error) { return guardTryRemoval(ctx, app, path, assume) }},
	})
}

func guardTryRemoval(ctx context.Context, app *App, path string, assume bool) (string, error) {
	if app.Tasks == nil {
		return "", errors.New("task inventory is unavailable")
	}
	tasks, diagnostics, err := app.Tasks.ListWithDiagnostics()
	if err != nil {
		return "", err
	}
	if len(diagnostics) > 0 {
		return "", errors.New("task inventory is incomplete")
	}
	for _, t := range tasks {
		for _, claimed := range []string{t.RepoPath, t.WorktreePath} {
			if claimed == "" {
				continue
			}
			within, err := pathx.Contains(path, claimed)
			if err != nil {
				return "", err
			}
			if within {
				return "", fmt.Errorf("Try is claimed by task %s; retire or recover that task first", t.ID)
			}
		}
	}
	rt := app.Runtime()
	if rt == nil || rt.Name() == "none" {
		if assume {
			return "unknown:explicit-assumption", nil
		}
		return "", errRemovalRuntimeUnknown
	}
	sessions, listErr := rt.List(ctx)
	unknown := listErr != nil
	for _, session := range sessions {
		paths := append([]string{}, session.Dirs...)
		paths = append(paths, session.WorkspaceCheckout)
		for _, pane := range session.Panes {
			paths = append(paths, pane.CWD, pane.ShellCWD)
		}
		observedPath := false
		for _, candidate := range paths {
			if candidate == "" {
				continue
			}
			observedPath = true
			within, err := pathx.Contains(path, candidate)
			if err != nil {
				unknown = true
				continue
			}
			if within {
				return "", fmt.Errorf("live %s session %s covers the Try; close it before deletion", rt.Name(), session.Handle)
			}
		}
		if !observedPath {
			unknown = true
		}
	}
	if unknown {
		if assume {
			return "unknown:explicit-assumption", nil
		}
		return "", errors.Join(errRemovalRuntimeUnknown, listErr)
	}
	return rt.Name() + ":observed-unoccupied", nil
}

func runTryDelete(ctx context.Context, app *App, ref string, opts tryDeleteOptions) error {
	if opts.confirmDelete != "" && !opts.permanent {
		return errors.New("--confirm-delete requires --permanent")
	}
	service, err := newRemovalService(app, opts.assumeNoRuntime)
	if err != nil {
		return err
	}
	plan, err := service.PlanRemoval(ctx, experiment.RemovalRequest{Ref: ref, Permanent: opts.permanent, Expected: opts.expected})
	if errors.Is(err, errRemovalRuntimeUnknown) && app.interactive() && !opts.json && !opts.dryRun {
		fmt.Fprintf(app.Out, "Try %s at %s\n", plan.ID, plan.Source)
		fmt.Fprintln(app.Out, err)
		confirmed, promptErr := newPrompter(app).confirm("Confirm no process or agent is using this Try?", false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			return errPromptCanceled
		}
		opts.assumeNoRuntime = true
		service, err = newRemovalService(app, true)
		if err != nil {
			return err
		}
		plan, err = service.PlanRemoval(ctx, experiment.RemovalRequest{Ref: ref, Permanent: opts.permanent, Expected: opts.expected})
	}
	if err != nil {
		if opts.json {
			_ = json.NewEncoder(app.Out).Encode(map[string]any{"plan": plan, "error": err.Error(), "outcome": "not-applied"})
		}
		return err
	}
	if opts.json && opts.dryRun {
		return json.NewEncoder(app.Out).Encode(plan)
	}
	if !opts.json {
		fmt.Fprintf(app.Out, "Try %s\n  path    %s\n  method  %s\n  files   %d (%d logical bytes)\n", plan.ID, plan.Source, plan.Method, plan.Files, plan.Bytes)
		for _, warning := range plan.Warnings {
			fmt.Fprintln(app.Out, "  "+warning)
		}
	}
	if opts.dryRun {
		return nil
	}
	interactive := app.interactive() && !opts.json
	if opts.permanent {
		if opts.confirmDelete != plan.ID {
			if !interactive || opts.confirmDelete != "" {
				return fmt.Errorf("permanent deletion requires --confirm-delete %s", plan.ID)
			}
			answer, err := newPrompter(app).dangerLine("Type DELETE " + plan.ID + " to discard all contents")
			if err != nil {
				return err
			}
			if answer != "DELETE "+plan.ID {
				return errPromptCanceled
			}
		}
	} else if !opts.yes {
		if !interactive {
			return errors.New("moving to Trash requires --yes in non-interactive use")
		}
		confirmed, err := newPrompter(app).confirm("Move this Try to system Trash?", false)
		if err != nil {
			return err
		}
		if !confirmed {
			return errPromptCanceled
		}
	}
	var result experiment.RemovalResult
	// Serialize against dev-mediated task claims while the catalog transaction
	// rechecks its source and runtime. External writers remain outside dev locks.
	err = app.Tasks.WithLock(ctx, func(*task.Tx) error {
		var applyErr error
		result, applyErr = service.ApplyRemoval(ctx, plan)
		return applyErr
	})
	if opts.json {
		_ = json.NewEncoder(app.Out).Encode(struct {
			Result experiment.RemovalResult `json:"result"`
			Error  string                   `json:"error,omitempty"`
		}{result, errorText(err)})
	} else {
		fmt.Fprintf(app.Out, "%s · %s · %s\n", result.ID, result.Method, result.Outcome)
		if result.Journal != "" {
			fmt.Fprintln(app.Out, "  operation record: "+result.Journal)
		}
	}
	return err
}

func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func runTryRemovalWorkflow(ctx context.Context, app *App, request tui.WorkflowRequest) error {
	if request.Try.Item.ID == "" {
		return errors.New("Try selection is required")
	}
	if request.Action == "restore-removed-try" {
		fmt.Fprintln(app.Out, "Restore the folder using system Trash first, or select the unchanged original after a failed Trash operation. dev verifies its original identity and contents.")
		path, err := newPrompter(app).line("Restored folder", "")
		if err != nil {
			return err
		}
		if path == "" {
			return errPromptCanceled
		}
		service, err := newExperimentService(app)
		if err != nil {
			return err
		}
		_, err = service.RestoreRemoved(ctx, request.Try.Item.ID, config.Expand(path))
		return err
	}
	return runTryDelete(ctx, app, request.Try.Item.ID, tryDeleteOptions{permanent: request.Action == "delete-try-permanently", expected: request.Try.Item.Entry})
}

// Keep restore paths explicit; empty paths must not resolve to the caller cwd.
func restoreRemovedTry(ctx context.Context, app *App, ref, from string) error {
	if from == "" {
		return errors.New("--from requires a restored directory")
	}
	service, err := newExperimentService(app)
	if err != nil {
		return err
	}
	item, err := service.RestoreRemoved(ctx, ref, filepath.Clean(config.Expand(from)))
	if err == nil {
		fmt.Fprintf(app.Out, "restored identity %s at %s\n", item.ID, item.Live.CurrentPath)
	}
	return err
}
