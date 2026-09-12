package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/feedback"
	"github.com/daviddwlee84/dev-cli/internal/hygiene"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/spf13/cobra"
)

func newHygieneManageCmd(h *hygieneCLI) *cobra.Command {
	var all bool
	cmd := &cobra.Command{Use: "manage [repo...]", Short: "Preview and set up hygiene across selected repositories", Args: cobra.ArbitraryArgs,
		Long: `Choose repositories, inspect per-repository plans and apply selected hygiene setup.
Existing equivalent scanner hooks can be migrated after review. Artifact
finalizers, custom rules and unrelated hooks remain. --json is preview-only;
apply a saved child plan with hygiene setup --repo PATH --apply --plan ID --yes.
This installs a normal commit check, not an agent skill. No tool dependency is
installed automatically.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			refs := append([]string(nil), args...)
			if h.repo != "" {
				refs = append(refs, h.repo)
			}
			_, err := runHygieneManage(cmd.Context(), h.app, refs, all, h.json)
			if errors.Is(err, errPromptCanceled) || errors.Is(err, picker.ErrCanceled) {
				fmt.Fprintln(h.app.Out, "Canceled.")
				return nil
			}
			return err
		}}
	cmd.Flags().BoolVar(&all, "all", false, "choose from configured repositories, including those without skills locks")
	return cmd
}

func runHygieneManage(ctx context.Context, app *App, refs []string, all, jsonOutput bool) (hygiene.BatchReceipt, error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	var receipt hygiene.BatchReceipt
	if all && len(refs) != 0 {
		return receipt, errors.New("choose repository arguments or --all")
	}
	if !jsonOutput && !app.canPick() {
		return receipt, errors.New("hygiene manage requires an interactive terminal; use --json to preview")
	}
	var targets []agenttarget.Target
	var err error
	if all {
		targets, err = agenttarget.All(ctx, app.Cfg.DiscoveryRoots())
	} else if len(refs) == 0 {
		var target agenttarget.Target
		target, err = agenttarget.Current(ctx, mustGetwd())
		if err == nil {
			targets = []agenttarget.Target{target}
		}
	} else {
		for _, ref := range refs {
			target, e := agenttarget.ResolveRepository(ctx, app.Cfg.DiscoveryRoots(), ref)
			if e != nil {
				return receipt, e
			}
			targets = append(targets, target)
		}
	}
	if err != nil {
		return receipt, err
	}
	targets = agenttarget.Dedupe(targets)
	if len(targets) == 0 {
		return receipt, errors.New("no repositories found")
	}
	if !jsonOutput && len(targets) > 1 {
		items := []picker.Item{}
		for i, t := range targets {
			items = append(items, picker.Item{Value: strconv.Itoa(i), Label: t.RepoDisplay, Description: config.Contract(t.CheckoutRoot)})
		}
		chosen, e := skillPick(ctx, app, "Repositories for hygiene setup", items, true, nil)
		if e != nil {
			return receipt, e
		}
		selected := []agenttarget.Target{}
		for _, item := range chosen {
			i, e := strconv.Atoi(item.Value)
			if e != nil || i < 0 || i >= len(targets) {
				return receipt, errors.New("invalid repository selection")
			}
			selected = append(selected, targets[i])
		}
		targets = selected
	}
	options := []hygiene.Options{}
	for _, t := range targets {
		options = append(options, hygiene.Options{Root: t.CheckoutRoot, StateDir: app.Cfg.StateDir(), CacheDir: sshDiscoveryCacheDir(), GlobalPolicy: filepath.Join(config.ConfigHome(), "dev", "hygiene.toml")})
	}
	batch, err := hygiene.PreviewSetupBatch(ctx, options, hygiene.SetupOptions{MigrateHooks: true, UpdateRules: true})
	h := &hygieneCLI{app: app, json: jsonOutput}
	if jsonOutput {
		return receipt, h.output(batch, err)
	}
	if err != nil {
		return receipt, err
	}
	items := []picker.Item{}
	for _, entry := range batch.Entries {
		fmt.Fprintf(app.Out, "\n%s · %s\n", config.Contract(entry.Root), entry.Status)
		if entry.Detail != "" {
			fmt.Fprintln(app.Out, feedback.Sanitize(entry.Detail))
		}
		if entry.Plan == nil {
			continue
		}
		if err := h.output(*entry.Plan, nil); err != nil {
			return receipt, err
		}
		items = append(items, picker.Item{Value: entry.Plan.ID, Label: config.Contract(entry.Root), Description: fmt.Sprintf("%d config changes · %s", len(entry.Plan.Files), entry.Plan.HookAction)})
	}
	if len(items) == 0 {
		return receipt, errors.New("no eligible hygiene setup plans; inspect reported blockers")
	}
	chosen, err := skillPick(ctx, app, "Setup plans to apply", items, true, nil)
	if err != nil {
		return receipt, err
	}
	ids := []string{}
	for _, item := range chosen {
		ids = append(ids, item.Value)
	}
	yes, err := newPrompter(app).confirm(fmt.Sprintf("Apply %d reviewed repository setup plan(s)", len(ids)), false)
	if err != nil {
		return receipt, err
	}
	if !yes {
		return receipt, errPromptCanceled
	}
	receipt, err = hygiene.ApplySetupBatch(ctx, batch, ids, hygiene.ApplyOptions{Guard: h.guard})
	for _, result := range receipt.Outcomes {
		fmt.Fprintf(app.Out, "[%s] %s\n", result.Status, config.Contract(result.Root))
		if result.Detail != "" {
			fmt.Fprintln(app.Out, "  "+feedback.Sanitize(result.Detail))
		}
	}
	if receipt.ReceiptPath != "" {
		fmt.Fprintln(app.Out, "Receipt: "+receipt.ReceiptPath)
	}
	return receipt, err
}

func (w *tuiWorkflow) runHygieneManagement() error {
	w.result.Scoped = true
	receipt, err := runHygieneManage(w.ctx, &w.app, w.request.RepoRefs, w.request.AllLocal, false)
	w.result.Status = "Returned from repository hygiene setup"
	if len(receipt.Outcomes) > 0 {
		w.result.Status = "Repository hygiene results saved"
	}
	if err != nil && !errors.Is(err, errPromptCanceled) && !errors.Is(err, picker.ErrCanceled) {
		w.result.Severity = "error"
	}
	if w.app.canPick() {
		_, pause := newPrompter(&w.app).line("Enter to return to dashboard", "")
		if err == nil {
			err = pause
		}
	}
	return err
}
