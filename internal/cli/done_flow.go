package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type doneIntegration string

const (
	doneIntegrationNone    doneIntegration = ""
	doneIntegrationFF      doneIntegration = "ff"
	doneIntegrationPR      doneIntegration = "pr"
	doneIntegrationMerged  doneIntegration = "merged"
	doneIntegrationCleanup doneIntegration = "cleanup"
)

type doneDirtyPolicy string

const (
	doneDirtyAuto    doneDirtyPolicy = "auto"
	doneDirtyFail    doneDirtyPolicy = "fail"
	doneDirtyCommit  doneDirtyPolicy = "commit"
	doneDirtyDiscard doneDirtyPolicy = "discard"
)

type doneOptions struct {
	Integration   doneIntegration
	DirtyPolicy   doneDirtyPolicy
	Message       string
	Yes           bool
	Push          bool
	BaseRef       string
	ConfirmSquash string
}

type donePlan struct {
	Integration doneIntegration
	DirtyAction doneDirtyPolicy
	Message     string
	Analysis    gitx.FinishAnalysis
	Prompted    bool
}

func runDone(ctx context.Context, app *App, args []string, opts doneOptions) error {
	if opts.DirtyPolicy == "" {
		opts.DirtyPolicy = doneDirtyAuto
	}
	switch opts.DirtyPolicy {
	case doneDirtyAuto, doneDirtyFail, doneDirtyCommit, doneDirtyDiscard:
	default:
		return fmt.Errorf("--dirty %q: want auto, fail, commit or discard", opts.DirtyPolicy)
	}
	if opts.Message != "" && opts.DirtyPolicy != doneDirtyCommit && opts.DirtyPolicy != doneDirtyAuto {
		return errors.New("--message is only used with --dirty=commit")
	}

	t, err := resolveTask(app, args)
	if err != nil {
		return err
	}
	checkout := checkoutOf(t)
	if _, err := os.Stat(checkout); err != nil {
		return fmt.Errorf("%s no longer exists — resume the task first", config.Contract(checkout))
	}
	if err := ensureArtifactsFinalized(app, checkout); err != nil {
		return err
	}
	mode := t.EffectiveMode()
	if mode == task.ModeDirect && opts.Integration != doneIntegrationNone {
		return fmt.Errorf("direct task %s is already on %s; it has no branch/worktree to integrate. "+
			"Run `dev done` without --ff/--pr/--merged", t.Title(), t.Branch)
	}
	base := t.Branch
	if mode != task.ModeDirect {
		base = t.Base
		if base == "" {
			base = gitx.DefaultBranch(ctx, t.RepoPath)
		}
		if base == "" {
			return fmt.Errorf("cannot determine the base branch for %s — pass --base when starting a task", t.Repo)
		}
	}

	analysis, err := gitx.AnalyzeFinish(ctx, checkout, base, t.Branch)
	if err != nil {
		return err
	}
	if analysis.Status.Conflicted > 0 {
		return fmt.Errorf("%s has %d conflicted path(s); resolve or abort the merge/rebase before finishing",
			config.Contract(checkout), analysis.Status.Conflicted)
	}
	interactive := app.interactive()
	var p *prompter
	if interactive {
		p = newPrompter(app)
		if opts.Integration == doneIntegrationNone || analysis.Status.Dirty() {
			renderDonePreflight(app, t, base, analysis)
		}
	}
	if mode != task.ModeDirect && opts.Integration == doneIntegrationNone && !interactive {
		renderDonePreflight(app, t, base, analysis)
		fmt.Fprintln(app.Out, "Nothing done. Choose an integration mode:")
		fmt.Fprintln(app.Out, "  dev done --ff      rebase when needed, then fast-forward "+base)
		fmt.Fprintln(app.Out, "  dev done --pr      push and open a pull request")
		fmt.Fprintln(app.Out, "  dev done --merged  verify a branch that was merged outside dev")
		return nil
	}

	plan, err := buildDonePlan(p, t, base, analysis, opts, interactive)
	if errors.Is(err, errPromptCanceled) {
		fmt.Fprintln(app.Out, "Canceled; nothing was changed.")
		return nil
	}
	if err != nil {
		return err
	}
	if plan.Prompted && !opts.Yes {
		confirmed, err := confirmDonePlan(app, p, t, base, plan)
		if errors.Is(err, errPromptCanceled) || !confirmed {
			fmt.Fprintln(app.Out, "Canceled; nothing was changed.")
			return nil
		}
		if err != nil {
			return err
		}
	}

	fresh, err := gitx.AnalyzeFinish(ctx, checkout, base, t.Branch)
	if err != nil {
		return err
	}
	if fresh.Fingerprint != plan.Analysis.Fingerprint || fresh.Relation != plan.Analysis.Relation {
		return errors.New("checkout or branch changed while the finish plan was open; review the new state and rerun dev done")
	}
	switch plan.DirtyAction {
	case doneDirtyCommit:
		if err := gitx.CommitAllChanges(ctx, checkout, plan.Message); err != nil {
			return fmt.Errorf("commit dirty checkout: %w", err)
		}
	case doneDirtyDiscard:
		if err := gitx.DiscardAllChanges(ctx, checkout); err != nil {
			return fmt.Errorf("discard dirty checkout: %w", err)
		}
	}
	analysis, err = gitx.AnalyzeFinish(ctx, checkout, base, t.Branch)
	if err != nil {
		return err
	}
	if analysis.Status.Dirty() {
		return fmt.Errorf("%s changed again during finalization: %s; stop the active writer and rerun dev done",
			config.Contract(checkout), analysis.Status.Breakdown())
	}

	if mode == task.ModeDirect {
		return finishDirectTask(ctx, app, t, checkout, opts.Push)
	}
	if plan.Integration == doneIntegrationPR {
		if analysis.Relation.Contained() {
			return fmt.Errorf("%s is already contained in %s; use --ff to finalize cleanup instead of opening a PR", t.Branch, base)
		}
		if err := openPR(ctx, app, t, checkout, base); err != nil {
			return err
		}
		fmt.Fprintln(app.Out, "\nREADY FOR REVIEW · runtime and worktree kept")
		fmt.Fprintln(app.Out, "After merge: dev done --merged --base-ref origin/"+base)
		return nil
	}
	if plan.Integration == doneIntegrationMerged {
		verifyBase := opts.BaseRef
		if verifyBase == "" {
			verifyBase = base
		}
		proof := t.Branch
		if opts.ConfirmSquash != "" {
			proof = opts.ConfirmSquash
			app.warnf("accepting operator attestation that squash commit %s represents branch %s", opts.ConfirmSquash, t.Branch)
		}
		if _, err := gitx.Run(ctx, t.RepoPath, "merge-base", "--is-ancestor", proof, verifyBase); err != nil {
			return fmt.Errorf("cannot verify %s is contained in %s", proof, verifyBase)
		}
		return finishIntegratedTask(ctx, app, t, checkout, verifyBase, opts)
	}
	if plan.Integration == doneIntegrationFF {
		// Idempotent: a branch already in the base needs no rebase, only the record.
		if _, err := gitx.Run(ctx, t.RepoPath, "merge-base", "--is-ancestor", t.Branch, base); err == nil {
			fmt.Fprintf(app.Out, "   already merged  %s is contained in %s\n", t.Branch, base)
			return finishIntegratedTask(ctx, app, t, checkout, base, opts)
		}
	}
	if err := fastForward(ctx, app, t, checkout, base); err != nil {
		return err
	}
	return finishIntegratedTask(ctx, app, t, checkout, base, opts)
}

func buildDonePlan(p *prompter, t *task.Task, base string, analysis gitx.FinishAnalysis, opts doneOptions, interactive bool) (donePlan, error) {
	plan := donePlan{Integration: opts.Integration, Analysis: analysis}
	if analysis.Status.Dirty() {
		switch opts.DirtyPolicy {
		case doneDirtyFail:
			return plan, dirtyFinishError(t, base, analysis)
		case doneDirtyAuto:
			if !interactive {
				return plan, dirtyFinishError(t, base, analysis)
			}
			choice, err := p.choice("Dirty changes (c=commit, d=discard, q=cancel)", "cancel",
				"commit (c), discard (d), cancel (q)", map[string]string{
					"c": "commit", "commit": "commit",
					"d": "discard", "discard": "discard", "drop": "discard",
					"q": "cancel", "cancel": "cancel",
				})
			if err != nil {
				return plan, err
			}
			if choice == "cancel" {
				return plan, errPromptCanceled
			}
			plan.DirtyAction, plan.Prompted = doneDirtyPolicy(choice), true
		default:
			plan.DirtyAction = opts.DirtyPolicy
			if plan.DirtyAction == doneDirtyDiscard && interactive && !opts.Yes {
				plan.Prompted = true
			}
		}
		if plan.DirtyAction == doneDirtyCommit {
			plan.Message = strings.TrimSpace(opts.Message)
			if plan.Message == "" {
				if !interactive {
					return plan, errors.New("--message is required with --dirty=commit outside an interactive terminal")
				}
				message, err := p.line("Commit message", "chore: finalize "+t.Title())
				if err != nil {
					return plan, err
				}
				plan.Message, plan.Prompted = strings.TrimSpace(message), true
			}
		}
		if plan.DirtyAction == doneDirtyDiscard && !interactive && !opts.Yes {
			return plan, errors.New("--dirty=discard outside an interactive terminal requires --yes")
		}
	}

	if t.EffectiveMode() == task.ModeDirect {
		plan.Integration = doneIntegrationCleanup
		return plan, nil
	}
	if plan.Integration == doneIntegrationMerged {
		return plan, nil
	}
	if plan.Integration == doneIntegrationPR && analysis.Relation.Contained() && plan.DirtyAction != doneDirtyCommit {
		return plan, fmt.Errorf("%s is already contained in %s; use --ff to finalize cleanup instead of opening a PR", t.Branch, base)
	}
	if plan.Integration == doneIntegrationNone {
		if analysis.Relation.Contained() && plan.DirtyAction != doneDirtyCommit {
			plan.Integration, plan.Prompted = doneIntegrationCleanup, true
			return plan, nil
		}
		if !interactive {
			return plan, errors.New("choose --ff, --pr or --merged")
		}
		choice, err := p.choice("Integration (f=fast-forward, p=PR, q=cancel)", "ff",
			"fast-forward (f), pull request (p), cancel (q)", map[string]string{
				"f": "ff", "ff": "ff", "fast-forward": "ff",
				"p": "pr", "pr": "pr", "pull-request": "pr",
				"q": "cancel", "cancel": "cancel",
			})
		if err != nil {
			return plan, err
		}
		if choice == "cancel" {
			return plan, errPromptCanceled
		}
		plan.Integration, plan.Prompted = doneIntegration(choice), true
	}
	return plan, nil
}

func dirtyFinishError(t *task.Task, base string, analysis gitx.FinishAnalysis) error {
	return fmt.Errorf("%s has uncommitted changes: %s; branch relation to %s is behind %d, ahead %d; "+
		"%d dirty path(s) match %s and %d contain unique content",
		config.Contract(checkoutOf(t)), analysis.Status.Breakdown(), base,
		analysis.Relation.BaseOnly, analysis.Relation.BranchOnly,
		analysis.EquivalentDirty(), base, analysis.UniqueDirty())
}

func renderDonePreflight(app *App, t *task.Task, base string, analysis gitx.FinishAnalysis) {
	s := app.outStyle()
	fmt.Fprintf(app.Out, "%s %s\n", s.title("Finish"), t.Title())
	fmt.Fprintf(app.Out, "  %s      %s\n", s.label("branch"), t.Branch)
	fmt.Fprintf(app.Out, "  %s        %s\n", s.label("base"), base)
	switch {
	case analysis.Relation.BaseOnly == 0 && analysis.Relation.BranchOnly == 0:
		fmt.Fprintf(app.Out, "  %s     %s\n", s.label("commits"), s.success(fmt.Sprintf("already equal to %s (behind 0, ahead 0)", base)))
	case analysis.Relation.Contained():
		fmt.Fprintf(app.Out, "  %s     %s\n", s.label("commits"), s.success(fmt.Sprintf("already contained in %s (behind %d, ahead 0)", base, analysis.Relation.BaseOnly)))
	default:
		fmt.Fprintf(app.Out, "  %s     %s\n", s.label("commits"), s.warning(fmt.Sprintf("behind %d, ahead %d relative to %s",
			analysis.Relation.BaseOnly, analysis.Relation.BranchOnly, base)))
	}
	if !analysis.Status.Dirty() {
		fmt.Fprintf(app.Out, "  %s    %s\n", s.label("checkout"), s.success("clean"))
		return
	}
	fmt.Fprintf(app.Out, "  %s    %s\n", s.label("checkout"), s.warning(analysis.Status.Breakdown()))
	fmt.Fprintf(app.Out, "  %s    %s, %s\n", s.label("contents"),
		s.success(fmt.Sprintf("%d match %s", analysis.EquivalentDirty(), base)),
		s.warning(fmt.Sprintf("%d unique", analysis.UniqueDirty())))
	for _, change := range analysis.Changes {
		marker := s.warning("unique")
		if change.BaseEquivalent {
			marker = s.success("matches " + base)
		}
		fmt.Fprintf(app.Out, "    %s%s %s\n", marker,
			strings.Repeat(" ", max(0, 12-width(marker))), change.DisplayPath())
	}
}

func confirmDonePlan(app *App, p *prompter, t *task.Task, base string, plan donePlan) (bool, error) {
	s := app.outStyle()
	fmt.Fprintln(app.Out, "\n"+s.title("Summary"))
	fmt.Fprintf(app.Out, "  %s        %s\n", s.label("task"), t.Title())
	switch plan.DirtyAction {
	case doneDirtyCommit:
		fmt.Fprintf(app.Out, "  %s       %s\n", s.label("dirty"), s.warning(fmt.Sprintf("commit all as %q", plan.Message)))
	case doneDirtyDiscard:
		fmt.Fprintf(app.Out, "  %s       %s\n", s.label("dirty"), s.danger("discard all staged, unstaged and untracked changes"))
	default:
		fmt.Fprintf(app.Out, "  %s       %s\n", s.label("dirty"), s.success("none"))
	}
	switch plan.Integration {
	case doneIntegrationPR:
		fmt.Fprintf(app.Out, "  %s   %s\n", s.label("integrate"), s.review("open a PR into "+base))
	case doneIntegrationCleanup:
		fmt.Fprintf(app.Out, "  %s   %s\n", s.label("integrate"), s.success("already contained in "+base+"; cleanup only"))
	default:
		fmt.Fprintf(app.Out, "  %s   %s\n", s.label("integrate"), s.success("fast-forward into "+base))
	}
	if plan.DirtyAction == doneDirtyDiscard && plan.Analysis.UniqueDirty() > 0 {
		value, err := p.dangerLine(fmt.Sprintf("Type DROP to discard %d unique path(s)", plan.Analysis.UniqueDirty()))
		if err != nil {
			return false, err
		}
		return value == "DROP", nil
	}
	return p.confirm("Proceed with this finish plan?", false)
}

func finishDirectTask(ctx context.Context, app *App, t *task.Task, checkout string, push bool) error {
	if push {
		if err := pushBranch(ctx, app, checkout, t.Branch); err != nil {
			return err
		}
	}
	t.State = task.Done
	if err := app.Tasks.Save(t); err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "%s %s completed directly on %s\n", task.Done.Icon(), t.Title(), t.Branch)
	fmt.Fprintln(app.Out, "   no branch or worktree was created or removed")
	fmt.Fprintln(app.Out, "   MERGED · cleanup pending: run dev retire from outside this runtime")
	return nil
}

func finishIntegratedTask(ctx context.Context, app *App, t *task.Task, checkout, base string, opts doneOptions) error {
	if opts.Push {
		if _, err := gitx.Run(ctx, t.RepoPath, "push", "origin", base); err != nil {
			app.warnf("could not push %s: %v", base, err)
		} else {
			fmt.Fprintf(app.Out, "   pushed     origin/%s\n", base)
		}
	}
	t.State = task.Done
	if err := app.Tasks.Save(t); err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "%s %s merged into %s\n", task.Done.Icon(), t.Title(), base)
	fmt.Fprintln(app.Out, "   MERGED · runtime and worktree kept")
	fmt.Fprintf(app.Out, "   cleanup pending · run `dev retire %s` from outside its workspace\n", t.ID)
	return nil
}
