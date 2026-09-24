package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/desktop"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/prflow"
	"github.com/spf13/cobra"
)

func prService(app *App, ref prflow.Reference) *prflow.Service {
	provider := app.prProvider
	if provider == nil {
		provider = forge.NewPRProvider(ref.Forge)
	}
	return &prflow.Service{Provider: provider, StateDir: app.Cfg.StateDir()}
}

func prWriteJSON(out io.Writer, value any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func newPRViewCmd(app *App) *cobra.Command {
	var jsonOutput, web bool
	cmd := &cobra.Command{Use: "view <URL>", Short: "Inspect one pull request's checks, changes and merge readiness", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ref, err := prflow.ParseReference(args[0])
		if err != nil {
			return err
		}
		if web {
			if jsonOutput {
				return errors.New("--web and --json cannot be combined")
			}
			return desktop.OpenURL(cmd.Context(), ref.URL())
		}
		detail, err := prService(app, ref).Detail(cmd.Context(), ref)
		if err != nil {
			return err
		}
		var locals []prflow.LocalCheckout
		localService, localErr := prCheckoutService(cmd.Context(), app, ref, "")
		if localErr == nil {
			locals, localErr = localService.Local(cmd.Context(), detail)
		}
		if jsonOutput {
			return prWriteJSON(app.Out, struct {
				prflow.Detail
				Local      []prflow.LocalCheckout `json:"local,omitempty"`
				LocalError string                 `json:"local_error,omitempty"`
			}{detail, locals, prErrorText(localErr)})
		}
		renderPRDetail(app, detail)
		for _, local := range locals {
			if local.Path != "" {
				fmt.Fprintf(app.Out, "local: %s · branch %s · differs %t\n", config.Contract(local.Path), terminalText(local.Branch), local.Differs)
			}
		}
		if localErr != nil {
			app.warnf("local checkout observations: %v", localErr)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit structured observations")
	cmd.Flags().BoolVar(&web, "web", false, "open the exact request in the browser")
	return cmd
}

func renderPRDetail(app *App, d prflow.Detail) {
	fmt.Fprintf(app.Out, "%s#%d  %s\n%s\n", d.Reference.Repo, d.Reference.Number, terminalText(d.Title), d.Reference.URL())
	fmt.Fprintf(app.Out, "state: %s · draft: %t · checks: %s · review: %s\n", d.State, d.Draft, prKnown(d.Checks), prKnown(d.ReviewDecision))
	fmt.Fprintf(app.Out, "%s → %s\nhead: %s\nbase: %s\n", terminalText(d.HeadBranch), terminalText(d.BaseBranch), d.HeadOID, d.BaseOID)
	fmt.Fprintf(app.Out, "changes: %s files · +%s −%s\n", prCount(d.Size.Files), prCount(d.Size.Additions), prCount(d.Size.Deletions))
	fmt.Fprintf(app.Out, "merge: %s", prKnown(d.Readiness))
	if d.Reason != "" {
		fmt.Fprintf(app.Out, " — %s", terminalText(d.Reason))
	}
	fmt.Fprintln(app.Out)
	if d.AutoDeleteBranch {
		fmt.Fprintln(app.Out, "Repository policy may delete the remote source branch after merging.")
	}
	for _, check := range d.CheckDetails {
		fmt.Fprintf(app.Out, "  %s  %s  %s\n", terminalText(check.State), terminalText(check.Name), terminalText(check.URL))
	}
	fmt.Fprintf(app.Out, "observed: %s\n", d.ObservedAt.Format(time.RFC3339))
}

func prKnown(value string) string {
	if value == "" {
		return "unknown"
	}
	return terminalText(value)
}
func prCount(value *int) string {
	if value == nil {
		return "?"
	}
	return fmt.Sprint(*value)
}

func terminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}

func newPRDiffCmd(app *App) *cobra.Command {
	var noPager, web bool
	cmd := &cobra.Command{Use: "diff <URL>", Short: "Preview a request diff with diffnav, or write it to stdout", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ref, err := prflow.ParseReference(args[0])
		if err != nil {
			return err
		}
		if web {
			return desktop.OpenURL(cmd.Context(), prDiffURL(ref))
		}
		service := prService(app, ref)
		detail, err := service.Detail(cmd.Context(), ref)
		if err != nil {
			return err
		}
		diff, err := service.Diff(cmd.Context(), detail)
		if err != nil {
			return err
		}
		if !diff.Complete && !app.interactive() {
			return errors.New("provider diff is incomplete; open the request in the browser or fetch an exact local checkout")
		}
		if diff.Warning != "" {
			fmt.Fprintf(app.Err, "diff preview: %s\n", terminalText(diff.Warning))
		}
		if !diff.Complete && diff.Warning == "" {
			fmt.Fprintln(app.Err, "Partial provider diff preview; open the browser or use an exact local checkout for complete contents.")
		}
		if !noPager && app.interactive() {
			if bin, lookupErr := exec.LookPath("diffnav"); lookupErr == nil {
				fmt.Fprintf(app.Err, "Remote diff preview: %s (live; merge revalidates independently)\n", ref.URL())
				process := exec.CommandContext(cmd.Context(), bin)
				process.Stdin, process.Stdout, process.Stderr = strings.NewReader(diff.TerminalText), app.Out, app.Err
				return process.Run()
			}
			fmt.Fprintln(app.Err, "diffnav is unavailable; writing the diff to stdout.")
		}
		text := diff.Text
		if app.interactive() {
			text = diff.TerminalText
		}
		_, err = io.WriteString(app.Out, text)
		return err
	}}
	cmd.Flags().BoolVar(&noPager, "no-pager", false, "write diff directly without starting diffnav")
	cmd.Flags().BoolVar(&web, "web", false, "open the request diff in the browser")
	return cmd
}

func prDiffURL(ref prflow.Reference) string {
	if ref.Forge == forge.GitLab {
		return ref.URL() + "/diffs"
	}
	return ref.URL() + "/files"
}

func prRepoReference(raw string) (prflow.Reference, error) {
	selectors, err := normalizePRSelectors([]string{raw})
	if err != nil {
		return prflow.Reference{}, err
	}
	s := selectors[0]
	if s.Kind == forge.Unknown {
		return prflow.Reference{}, errors.New("--scope repo needs a forge URL or github:owner/repo / gitlab:group/repo")
	}
	if s.Host == "" {
		s.Host = forge.ConfiguredHost(s.Kind)
	}
	ref := prflow.Reference{Forge: s.Kind, Host: s.Host, Repo: s.Name}
	return ref, ref.Validate()
}

func runPRRepoList(ctx context.Context, app *App, opts prFlags, query string) error {
	if len(opts.Repos) != 1 {
		return errors.New("--scope repo requires exactly one --repo")
	}
	if opts.AllRepos {
		return errors.New("--all-repos cannot be combined with --scope repo")
	}
	if opts.PageSize < 1 || opts.PageSize > 100 || opts.Limit < 0 {
		return errors.New("--page-size must be 1-100 and --limit must be nonnegative")
	}
	ref, err := prRepoReference(opts.Repos[0])
	if err != nil {
		return err
	}
	state := forge.PRState(strings.ToLower(opts.State))
	switch state {
	case forge.PRStateOpen, forge.PRStateClosed, forge.PRStateMerged, forge.PRStateAll:
	default:
		return errors.New("--state must be open, closed, merged or all")
	}
	relationship := "all"
	if len(opts.Roles) > 0 {
		if len(opts.Roles) == 2 && ((opts.Roles[0] == "author" && opts.Roles[1] == "reviewer") || (opts.Roles[0] == "reviewer" && opts.Roles[1] == "author")) {
			relationship = "related"
		} else if len(opts.Roles) == 1 {
			relationship = opts.Roles[0]
		} else {
			return errors.New("--scope repo accepts --role all, author, reviewer or author,reviewer")
		}
	}
	switch relationship {
	case "all", "author", "reviewer", "related":
	default:
		return errors.New("unknown PR role")
	}
	page, err := prService(app, ref).ListPage(ctx, prflow.Query{Reference: ref, Relationship: relationship, State: state, Limit: opts.PageSize, Cursor: opts.Cursor})
	if err != nil && len(page.PullRequests) == 0 {
		return err
	}
	rows := filterPRRows(joinLocalCheckouts(ctx, app, page.PullRequests), query, opts)
	if opts.JSON {
		payload := makePRListJSON(rows, []prProviderStatus{{Forge: ref.Forge, Status: "ready"}}, prCollectOptions{Scope: prScope("repo"), Query: forge.PRQuery{State: state, AnyRole: relationship == "all"}, Repos: []prRepoSelector{{Kind: ref.Forge, Host: ref.Host, Name: ref.Repo}}}, err)
		// Add page metadata without changing the existing schema-version-1 rows.
		data, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return marshalErr
		}
		var fields map[string]any
		if decodeErr := json.Unmarshal(data, &fields); decodeErr != nil {
			return decodeErr
		}
		fields["pagination"] = map[string]any{"next_cursor": page.NextCursor, "complete": page.Complete, "total": page.Total, "total_lower_bound": page.TotalLowerBound, "relationship": page.Scope}
		if relationship == "all" {
			fields["roles"] = []string{"all"}
		} else if relationship == "related" {
			fields["roles"] = []string{"author", "reviewer"}
		} else {
			fields["roles"] = []string{relationship}
		}
		if writeErr := prWriteJSON(app.Out, fields); writeErr != nil {
			return writeErr
		}
	} else {
		renderPRTable(app, rows, []prProviderStatus{{Forge: ref.Forge, Status: "ready"}}, opts.Actions)
		if page.NextCursor != "" {
			fmt.Fprintf(app.Err, "More requests available; continue with --cursor %s\n", page.NextCursor)
		}
	}
	if err != nil {
		app.warnf("partial pull request page: %v", err)
	}
	return nil
}

func prConfirm(app *App, action string, yes bool) error {
	if yes {
		return nil
	}
	if !app.interactive() {
		return errors.New("this operation requires --yes outside an interactive terminal")
	}
	confirmed, err := newPrompter(app).confirm(action, false)
	if err != nil {
		return err
	}
	if !confirmed {
		return errPromptCanceled
	}
	return nil
}
