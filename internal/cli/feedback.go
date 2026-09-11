package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/feedback"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/spf13/cobra"
)

func feedbackStore(app *App) feedback.Store {
	return feedback.Store{Dir: filepath.Join(app.Cfg.StateDir(), "feedback")}
}
func feedbackReporter(app *App) forge.IssueReporter {
	if app.feedbackReporter != nil {
		return app.feedbackReporter
	}
	return forge.GitHubIssueReporter{}
}
func feedbackInvocation(cmd *cobra.Command) bool {
	for cur := cmd; cur != nil; cur = cur.Parent() {
		if cur.Name() == "feedback" {
			return true
		}
	}
	return false
}
func feedbackDraftInvocation(cmd *cobra.Command) bool {
	return cmd.Name() == "feedback" || cmd.Name() == "draft" && cmd.Parent() != nil && cmd.Parent().Name() == "feedback"
}
func feedbackError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(feedback.Sanitize(err.Error()))
}
func feedbackOutput(app *App, jsonOut bool, value any, err error) error {
	if jsonOut {
		encoder := json.NewEncoder(app.Out)
		encoder.SetIndent("", "  ")
		if e := encoder.Encode(value); e != nil {
			return e
		}
	}
	return feedbackError(err)
}
func newFeedbackCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "feedback", Short: "Prepare a local report, publish reviewed feedback, or plan an isolated fix", Long: `Keep a durable local report before deciding whether to publish an issue or
prepare a repair workspace. GitHub lookup/publication and agent launch are
explicit. Reports and repair prompts remain local; only reviewed public drafts
may be published. Missing gh or network access does not prevent local drafts.`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !app.interactive() {
			return cmd.Help()
		}
		return runFeedbackDraft(cmd.Context(), app, "", "", "", false)
	}}
	cmd.AddCommand(newFeedbackDraftCmd(app), newFeedbackIssueCmd(app), newFeedbackRepairCmd(app))
	return cmd
}
func newFeedbackDraftCmd(app *App) *cobra.Command {
	var title, body, diagnostic string
	var jsonOut bool
	cmd := &cobra.Command{Use: "draft", Short: "Save a private local report and a sanitized public issue draft", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runFeedbackDraft(cmd.Context(), app, title, body, diagnostic, jsonOut)
	}}
	cmd.Flags().StringVar(&title, "title", "", "short report title")
	cmd.Flags().StringVar(&body, "body-file", "", "Markdown reproduction/expected/actual body file; - reads stdin")
	cmd.Flags().StringVar(&diagnostic, "diagnostic", "", "local schema-v1 ssh diagnose JSON to project into public evidence")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the saved report ID, paths and next commands")
	return cmd
}
func feedbackReadInput(ctx context.Context, app *App, path string) ([]byte, error) {
	if path == "-" {
		data, err := io.ReadAll(io.LimitReader(app.In, feedback.MaxReportBytes+1))
		if err != nil || len(data) > feedback.MaxReportBytes {
			return nil, errors.New("feedback input is unreadable or exceeds 1 MiB")
		}
		return data, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("invalid feedback input path")
	}
	root, _, err := safefile.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, errors.New("feedback input directory unavailable")
	}
	defer root.Close()
	data, _, err := safefile.ReadStableRegular(ctx, root, filepath.Base(absolute), nil, feedback.MaxReportBytes)
	if err != nil {
		return nil, errors.New("feedback input must be a bounded regular file")
	}
	return data, nil
}
func runFeedbackDraft(ctx context.Context, app *App, title, bodyPath, diagnosticPath string, jsonOut bool) error {
	var body []byte
	var err error
	if title == "" || bodyPath == "" {
		if jsonOut || !app.interactive() {
			return asUsageError(errors.New("provide --title and --body-file (use - for stdin)"))
		}
		prompt := newPrompter(app)
		if title == "" {
			title, err = prompt.line("Report title", "")
			if err != nil {
				return err
			}
		}
		if bodyPath == "" {
			repro, e := prompt.line("Minimal reproduction", "")
			if e != nil {
				return e
			}
			expected, e := prompt.line("Expected behavior", "")
			if e != nil {
				return e
			}
			actual, e := prompt.line("Actual behavior", "")
			if e != nil {
				return e
			}
			body = []byte("## Reproduction\n\n" + repro + "\n\n## Expected\n\n" + expected + "\n\n## Actual\n\n" + actual)
		}
	}
	if bodyPath != "" {
		body, err = feedbackReadInput(ctx, app, bodyPath)
		if err != nil {
			return err
		}
	}
	var diagnostic []byte
	if diagnosticPath != "" {
		if bodyPath == "-" && diagnosticPath == "-" {
			return asUsageError(errors.New("stdin may supply only one input"))
		}
		diagnostic, err = feedbackReadInput(ctx, app, diagnosticPath)
		if err != nil {
			return err
		}
	}
	method := "unknown"
	if install, e := runningInstall(); e == nil {
		switch install.Method {
		case methodHomebrew:
			method = "homebrew"
		case methodScoop:
			method = "scoop"
		case methodGo:
			method = "go-install"
		case methodStandalone:
			method = "standalone"
		}
	}
	result, err := feedbackStore(app).Create(ctx, feedback.DraftRequest{Title: strings.TrimSpace(title), Body: string(body), Diagnostic: diagnostic, Facts: feedback.Facts{Version: versionFromBuild(), OS: runtime.GOOS, Arch: runtime.GOARCH, Installation: method}})
	if !jsonOut && result.Report.ID != "" {
		fmt.Fprintf(app.Out, "Feedback %s\nReview public draft: %s\nPrivate context: %s\n", result.Report.ID, result.Report.DraftPath, result.Report.ContextPath)
		for _, next := range result.Next {
			fmt.Fprintln(app.Out, next)
		}
		fmt.Fprintln(app.Out, "Review free text for identifying names before publishing. No issue or agent has been started.")
	}
	return feedbackOutput(app, jsonOut, result, err)
}
func newFeedbackIssueCmd(app *App) *cobra.Command {
	var request feedback.IssueRequest
	var jsonOut, search, publish, yes bool
	cmd := &cobra.Command{Use: "issue <id>", Short: "Preview, search for, or explicitly publish reviewed GitHub feedback", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request.ID = args[0]
		if search && publish || yes && !publish || request.Existing < 0 || cmd.Flags().Changed("existing") && request.Existing == 0 {
			return asUsageError(errors.New("--search and --publish are separate; --yes requires --publish; --existing must be positive"))
		}
		store := feedbackStore(app)
		if publish {
			if yes && request.Revision == "" {
				return asUsageError(errors.New("--yes requires --revision from the reviewed issue preview"))
			}
			if !yes {
				if jsonOut || !app.interactive() {
					return asUsageError(errors.New("noninteractive publication requires --yes --revision <preview-revision>"))
				}
				preview, err := store.PreviewIssue(cmd.Context(), request)
				if err != nil {
					return feedbackError(err)
				}
				if request.Revision != "" && request.Revision != preview.Revision {
					return feedback.ErrStale
				}
				renderFeedbackIssue(app, preview)
				ok, err := newPrompter(app).confirm("Publish this exact draft to the displayed GitHub target?", false)
				if err != nil {
					return err
				}
				if !ok {
					return errPromptCanceled
				}
				request.Revision = preview.Revision
			}
			result, err := store.PublishIssue(cmd.Context(), request, feedbackReporter(app))
			if !jsonOut {
				fmt.Fprintf(app.Out, "%s %s\n", result.Status, result.URL)
			}
			return feedbackOutput(app, jsonOut, result, err)
		}
		var preview feedback.IssuePreview
		var err error
		if search {
			preview, err = store.SearchIssues(cmd.Context(), request, feedbackReporter(app))
		} else {
			preview, err = store.PreviewIssue(cmd.Context(), request)
		}
		if !jsonOut && err == nil {
			renderFeedbackIssue(app, preview)
		}
		return feedbackOutput(app, jsonOut, preview, err)
	}}
	f := cmd.Flags()
	f.StringVar(&request.Repository, "repo", forge.DefaultIssueRepository, "explicit [host/]owner/name issue target")
	f.IntVar(&request.Existing, "existing", 0, "comment on this exact existing issue instead of creating a new issue")
	f.BoolVar(&search, "search", false, "explicitly query related GitHub issues")
	f.BoolVar(&publish, "publish", false, "publish the reviewed issue or comment")
	f.BoolVar(&yes, "yes", false, "confirm publication of the supplied revision without prompting")
	f.StringVar(&request.Revision, "revision", "", "exact content/target revision from preview")
	f.BoolVar(&jsonOut, "json", false, "emit one versioned preview or publication result")
	return cmd
}
func renderFeedbackIssue(app *App, p feedback.IssuePreview) {
	fmt.Fprintf(app.Out, "%s on %s", p.Operation, p.Target.String())
	if p.Existing > 0 {
		fmt.Fprintf(app.Out, " #%d", p.Existing)
	}
	fmt.Fprintf(app.Out, "\nRevision: %s\n\n%s\n", p.Revision, p.Body)
	for _, issue := range p.Candidates {
		fmt.Fprintf(app.Out, "Related #%d: %s %s\n", issue.Number, issue.Title, issue.URL)
	}
}
func newFeedbackRepairCmd(app *App) *cobra.Command {
	var request feedback.RepairRequest
	var planID string
	var apply, yes, jsonOut bool
	cmd := &cobra.Command{Use: "repair <id>", Short: "Plan or prepare an isolated repair checkout without starting an agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request.ReportID = args[0]
		backend := feedbackStartBackend{app}
		if yes && !apply || planID != "" && !apply {
			return asUsageError(errors.New("--yes and --plan require --apply"))
		}
		if apply {
			if planID == "" || request.Repository != "" || request.Base != "" {
				return asUsageError(errors.New("apply requires --plan <id>; source/base come only from that saved plan"))
			}
			if !yes {
				if jsonOut || !app.interactive() {
					return asUsageError(errors.New("noninteractive apply requires --yes"))
				}
				plan, err := feedbackStore(app).SavedRepairPlan(cmd.Context(), request.ReportID, planID)
				if err != nil {
					return feedbackError(err)
				}
				fmt.Fprintf(app.Out, "Source: %s\nBase: %s (%s)\nCheckout: %s\n", plan.Snapshot.Source.Path, plan.Snapshot.BaseRef, plan.Snapshot.BaseOID, plan.Snapshot.Checkout)
				for _, effect := range plan.Effects {
					fmt.Fprintln(app.Out, effect)
				}
				confirmed, err := newPrompter(app).confirm("Apply this repair workspace plan?", false)
				if err != nil {
					return err
				}
				if !confirmed {
					return errPromptCanceled
				}
			}
			result, err := feedbackStore(app).ApplyRepair(cmd.Context(), request.ReportID, planID, backend)
			if !jsonOut {
				fmt.Fprintln(app.Out, result.Status)
				if result.Binding != nil {
					fmt.Fprintln(app.Out, result.Binding.Snapshot.Checkout)
				}
				for _, next := range result.Next {
					fmt.Fprintln(app.Out, next)
				}
			}
			return feedbackOutput(app, jsonOut, result, err)
		}
		plan, err := feedbackStore(app).PlanRepair(cmd.Context(), request, backend)
		if !jsonOut {
			fmt.Fprintf(app.Out, "Repair: %s\n", plan.Status)
			for _, candidate := range plan.Candidates {
				fmt.Fprintf(app.Out, "Source: %s (%s, remote %s)\n", candidate.Path, candidate.Repository, candidate.Remote)
			}
			if plan.Snapshot != nil {
				fmt.Fprintf(app.Out, "Base: %s (%s)\nBranch: %s\nCheckout: %s\n", plan.Snapshot.BaseRef, plan.Snapshot.BaseOID, plan.Snapshot.Branch, plan.Snapshot.Checkout)
			}
			for _, effect := range plan.Effects {
				fmt.Fprintln(app.Out, "  "+effect)
			}
			if plan.ID != "" {
				fmt.Fprintf(app.Out, "Apply: dev feedback repair %s --apply --plan %s --yes\n", request.ReportID, plan.ID)
			}
			for _, next := range plan.Next {
				fmt.Fprintln(app.Out, next)
			}
		}
		return feedbackOutput(app, jsonOut, plan, err)
	}}
	cmd.Flags().StringVar(&request.Repository, "repo", "", "exact local dev-cli source checkout")
	cmd.Flags().StringVar(&request.Base, "base", "", "explicit local base ref for the repair branch")
	cmd.Flags().StringVar(&planID, "plan", "", "exact saved repair plan ID to apply")
	cmd.Flags().BoolVar(&apply, "apply", false, "create only the checkout/task described by the saved plan")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the saved repair plan without prompting")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit one versioned repair plan or result")
	return cmd
}
