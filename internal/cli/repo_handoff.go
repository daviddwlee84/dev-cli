package cli

import (
	"errors"
	"fmt"
)

type repoHandoff string

const (
	repoHandoffStay  repoHandoff = "stay"
	repoHandoffCD    repoHandoff = "cd"
	repoHandoffOpen  repoHandoff = "open"
	repoHandoffStart repoHandoff = "start"
)

func parseRepoHandoff(value string) (repoHandoff, error) {
	mode := repoHandoff(value)
	switch mode {
	case "", repoHandoffStay:
		return repoHandoffStay, nil
	case repoHandoffCD, repoHandoffOpen, repoHandoffStart:
		return mode, nil
	default:
		return "", fmt.Errorf("handoff %q: want stay, cd, open or start", value)
	}
}

func performRepoHandoff(app *App, path, label string, mode repoHandoff) error {
	ctx := ctxOf()
	switch mode {
	case repoHandoffStay, "":
		return nil
	case repoHandoffCD:
		return app.cdDirective(path)
	case repoHandoffOpen:
		rt := app.Runtime()
		if rt.Name() == "none" {
			return app.cdDirective(path)
		}
		opened, err := openCheckout(ctx, rt, path, label)
		if err != nil {
			return err
		}
		return activateRuntime(ctx, rt, opened.Handle)
	case repoHandoffStart:
		if !app.interactive() {
			return errors.New("handoff=start requires an interactive terminal; run dev start afterwards")
		}
		spec, confirmed, err := runStartWizard(ctx, app, startRequest{
			RepoRef: path, RepoExplicit: true, Focus: true,
		})
		if errors.Is(err, errPromptCanceled) || !confirmed {
			fmt.Fprintln(app.Out, "Repository is ready; task creation was canceled.")
			return nil
		}
		if err != nil {
			return err
		}
		result, err := executeStartSpec(ctx, app, spec, app.Err)
		if err != nil {
			return decorateStartError(err)
		}
		if result.Worktree != nil {
			reportProvision(app, result.Worktree)
		}
		fmt.Fprintf(app.Out, "%s %s  %s on %s (%s)\n",
			result.Task.State.Icon(), result.Task.Name, result.Task.Repo, result.Task.Branch, result.Task.Mode)
		if result.Runtime.Name() == "none" {
			return app.cdDirective(checkoutOf(result.Task))
		}
		return activateRuntime(ctx, result.Runtime, result.Task.RuntimeHandle)
	default:
		return fmt.Errorf("unsupported handoff %q", mode)
	}
}

func repoHandoffCompletions() []string {
	return []string{string(repoHandoffStay), string(repoHandoffCD), string(repoHandoffOpen), string(repoHandoffStart)}
}
