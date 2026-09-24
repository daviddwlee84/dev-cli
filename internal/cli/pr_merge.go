package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/prflow"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/spf13/cobra"
)

type prSyncFlags struct {
	Repo, Strategy    string
	Yes, DryRun, JSON bool
}
type prSyncLedger struct {
	Fetch    *prflow.BaseSyncResult `json:"fetch,omitempty"`
	BaseSync *prflow.BaseSyncResult `json:"base_sync,omitempty"`
	Plan     *prflow.BaseSyncPlan   `json:"plan,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

func newPRMergeCmd(app *App) *cobra.Command {
	var squash, yes, dryRun, jsonOutput bool
	var syncBase, repoRef string
	cmd := &cobra.Command{Use: "merge <URL>", Short: "Preview and perform one immediate squash merge", Long: `Merge only the freshly reviewed request head when the provider reports ready.
Queue, auto-merge and merge trains are handled in the provider's own UI.
An uncertain remote write is recorded privately and is never retried automatically.

Optional base synchronization runs only after confirmed merge, with a separate
fresh plan. It does not remove worktrees, Try clones or task records.`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !squash {
			return errors.New("select --squash; this command does not choose a merge strategy implicitly")
		}
		if syncBase != "none" && syncBase != "ff-only" && syncBase != "rebase" {
			return errors.New("--sync-base must be none, ff-only or rebase")
		}
		ref, err := prflow.ParseReference(args[0])
		if err != nil {
			return err
		}
		service := prService(app, ref)
		plan, err := service.PlanMerge(cmd.Context(), ref)
		if err != nil {
			return fmt.Errorf("%w; inspect %s", err, ref.URL())
		}
		if dryRun && jsonOutput {
			return prWriteJSON(app.Out, struct {
				Merge      prflow.MergePlan `json:"merge"`
				SyncBase   string           `json:"sync_base"`
				Repository string           `json:"repository,omitempty"`
			}{plan, syncBase, repoRef})
		}
		review := *app
		if jsonOutput {
			review.Out = app.Err
		}
		renderPRDetail(&review, plan.Detail)
		fmt.Fprintf(review.Out, "Squash plan %s\n", plan.ID)
		if syncBase != "none" {
			fmt.Fprintf(review.Out, "After confirmed merge: fetch and review %s synchronization of %s.\n", syncBase, terminalText(plan.Detail.BaseBranch))
		}
		if dryRun {
			return nil
		}
		if err = prConfirm(&review, "Squash this exact PR head?", yes); err != nil {
			return err
		}
		merged, mergeErr := service.ApplyMerge(cmd.Context(), plan)
		var syncResult *prSyncLedger
		if !jsonOutput {
			fmt.Fprintf(app.Out, "merge: %s", merged.Status)
			if merged.MergeOID != "" {
				fmt.Fprintf(app.Out, " · %s", merged.MergeOID)
			}
			fmt.Fprintln(app.Out)
			if merged.ReceiptPath != "" {
				fmt.Fprintf(app.Out, "receipt: %s\n", config.Contract(merged.ReceiptPath))
			}
		}
		if merged.Status == "merged" && mergeErr == nil && syncBase != "none" {
			ledger, syncErr := runPRBaseSync(cmd.Context(), &review, ref, prSyncFlags{Repo: repoRef, Strategy: syncBase, Yes: yes})
			syncResult = &ledger
			mergeErr = syncErr
		}
		if jsonOutput {
			writeErr := prWriteJSON(app.Out, struct {
				Merge prflow.MergeResult `json:"merge"`
				Sync  *prSyncLedger      `json:"sync,omitempty"`
				Error string             `json:"error,omitempty"`
			}{merged, syncResult, prErrorText(mergeErr)})
			return errors.Join(mergeErr, writeErr)
		}
		return mergeErr
	}}
	f := cmd.Flags()
	f.BoolVar(&squash, "squash", false, "explicitly squash the reviewed head")
	f.StringVar(&syncBase, "sync-base", "none", "after confirmed merge, update the local base: none, ff-only or rebase")
	f.StringVar(&repoRef, "repo", "", "select the local repository for optional base synchronization")
	f.BoolVarP(&yes, "yes", "y", false, "approve the displayed merge and explicitly selected synchronization strategy")
	f.BoolVar(&dryRun, "dry-run", false, "preview without merging or fetching")
	f.BoolVar(&jsonOutput, "json", false, "emit structured stage results; confirmation still requires --yes outside a terminal")
	return cmd
}

func newPRSyncBaseCmd(app *App) *cobra.Command {
	opts := prSyncFlags{}
	cmd := &cobra.Command{Use: "sync-base <URL>", Short: "Update the local base of a confirmed merged request", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ref, err := prflow.ParseReference(args[0])
		if err != nil {
			return err
		}
		review := *app
		if opts.JSON {
			review.Out = app.Err
		}
		result, err := runPRBaseSync(cmd.Context(), &review, ref, opts)
		if opts.JSON {
			return errors.Join(err, prWriteJSON(app.Out, result))
		}
		return err
	}}
	f := cmd.Flags()
	f.StringVar(&opts.Repo, "repo", "", "select the local repository whose actual base checkout should be updated")
	f.StringVar(&opts.Strategy, "strategy", "ff-only", "base update strategy: ff-only or explicit rebase")
	f.BoolVarP(&opts.Yes, "yes", "y", false, "approve the displayed fetch and fresh base synchronization plans")
	f.BoolVar(&opts.DryRun, "dry-run", false, "preview the fetch stage; no refs or checkouts are changed")
	f.BoolVar(&opts.JSON, "json", false, "emit structured plan or stage results")
	return cmd
}

func runPRBaseSync(ctx context.Context, app *App, ref prflow.Reference, opts prSyncFlags) (ledger prSyncLedger, err error) {
	defer func() { ledger.Error = prErrorText(err) }()
	if opts.Strategy == "" {
		opts.Strategy = "ff-only"
	}
	if opts.Strategy != "ff-only" && opts.Strategy != "rebase" {
		return ledger, errors.New("--strategy must be ff-only or rebase")
	}
	provider := prService(app, ref)
	detail, err := provider.Detail(ctx, ref)
	if err != nil {
		return ledger, err
	}
	if string(detail.State) != "merged" || detail.MergeOID == "" {
		return ledger, errors.New("base synchronization requires a confirmed merged request")
	}
	local, err := prSelectSyncRepository(ctx, app, ref, detail, opts.Repo)
	if err != nil {
		return ledger, err
	}
	service, err := prflow.NewBaseSyncService(prflow.BaseSyncConfig{Tasks: app.Tasks, Host: config.Hostname(), Resolver: provider, Runtimes: func() []runtime.Runtime { return prSyncRuntimes(app) }})
	if err != nil {
		return ledger, err
	}
	req := prflow.BaseSyncRequest{Reference: ref, RepoPath: local, Mode: opts.Strategy}
	fetch, err := service.PlanFetch(ctx, req)
	if err != nil {
		return ledger, err
	}
	ledger.Plan = &fetch
	if !opts.JSON || !opts.DryRun {
		renderPRSyncPlan(app, fetch)
	}
	if opts.DryRun {
		return ledger, nil
	}
	if err = prConfirm(app, "Fetch the selected PR base remote?", opts.Yes); err != nil {
		return ledger, err
	}
	fetched, err := service.ApplyFetch(ctx, fetch)
	ledger.Fetch = &fetched
	if err != nil {
		return ledger, err
	}
	if !opts.JSON {
		fmt.Fprintln(app.Out, "fetch: completed")
	}
	plan, err := service.Plan(ctx, req)
	ledger.Plan = &plan
	if err != nil {
		return ledger, err
	}
	renderPRSyncPlan(app, plan)
	if !plan.NoOp && plan.Plan.Availability != taskflow.AvailabilityReady {
		return ledger, fmt.Errorf("base synchronization is %s; resolve the displayed conditions and retry sync-base", plan.Plan.Availability)
	}
	if !plan.NoOp {
		if err = prConfirm(app, "Apply this exact base synchronization?", opts.Yes); err != nil {
			return ledger, err
		}
	}
	result, err := service.Apply(ctx, plan)
	ledger.BaseSync = &result
	if !opts.JSON {
		if err == nil {
			if result.NoOp {
				fmt.Fprintln(app.Out, "base sync: already up to date; local commits retained")
			} else {
				fmt.Fprintf(app.Out, "base sync: %s completed · %s\n", result.Operation, result.NewOID)
			}
		}
		for _, recovery := range result.Recovery {
			fmt.Fprintf(app.Out, "  recovery: %s\n", terminalText(recovery))
		}
	}
	return ledger, err
}

func prSelectSyncRepository(ctx context.Context, app *App, ref prflow.Reference, detail prflow.Detail, explicit string) (string, error) {
	if explicit != "" {
		r, _, err := resolveRepoRef(app, explicit)
		return r.Path, err
	}
	service, err := prCheckoutService(ctx, app, ref, "")
	if err != nil {
		return "", err
	}
	locals, err := service.Local(ctx, detail)
	if err != nil {
		return "", err
	}
	paths := map[string]string{}
	for _, local := range locals {
		paths[local.GitCommonDir] = local.RepoPath
	}
	var choices []string
	for _, p := range paths {
		choices = append(choices, p)
	}
	sort.Strings(choices)
	if len(choices) == 0 {
		return "", errors.New("no local repository for base synchronization; use --repo")
	}
	if len(choices) == 1 {
		return choices[0], nil
	}
	if !app.interactive() {
		return "", errors.New("multiple local clones; use --repo to choose the base synchronization target")
	}
	for i, p := range choices {
		fmt.Fprintf(app.Out, "  %d  %s\n", i+1, config.Contract(p))
	}
	answer, err := newPrompter(app).line("Base repository number (or cancel)", "cancel")
	if err != nil {
		return "", err
	}
	if answer == "cancel" {
		return "", errPromptCanceled
	}
	i, err := strconv.Atoi(answer)
	if err != nil || i < 1 || i > len(choices) {
		return "", errors.New("choose a listed repository number")
	}
	return choices[i-1], nil
}

func prSyncRuntimes(app *App) []runtime.Runtime {
	if app.noRuntime || app.runtimeOverride == "none" || (app.runtimeOverride == "" && app.Cfg.Runtime.Backend == "none") {
		// Respect the no-multiplexer contract. An empty observation set leaves
		// checkout mutations unavailable rather than pretending they are clear.
		return nil
	}
	if app.runtimeInstance != nil {
		return []runtime.Runtime{app.runtimeInstance}
	}
	// Observe all installed backends when runtime observation is enabled.
	// runtime.None still contributes its local process observation.
	backends := []runtime.Runtime{runtime.None{}}
	for _, name := range []string{"herdr", "tmux", "zellij"} {
		backend := app.runtimeNamed(name)
		if backend.Available() {
			backends = append(backends, backend)
		}
	}
	return backends
}

func renderPRSyncPlan(app *App, p prflow.BaseSyncPlan) {
	fmt.Fprintf(app.Out, "%s %s/%s\n  checkout %s\n  %s → %s\n", p.Operation, p.Remote, terminalText(p.Branch), config.Contract(p.Path), p.OldOID, p.NewOID)
	if p.NoOp {
		fmt.Fprintf(app.Out, "  %s\n", terminalText(p.Reason))
		return
	}
	for _, condition := range p.Plan.BlockingConditions() {
		fmt.Fprintf(app.Out, "  blocked: %s · %s\n", terminalText(condition.Evidence), terminalText(condition.Remediation))
	}
}
