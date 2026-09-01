package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/spf13/cobra"
)

func newPRCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "List the pull requests waiting on you, and hand them to an agent",
		Long: `Show the pull and merge requests you opened and the ones asking for your review.

Opening a request usually ends a worktree's useful life, but nothing local says
so, and requests accumulate faster than anyone reviews them. This lists them in
one place and, where dev already has a checkout for the branch, says which one.

Nothing here changes anything: no approving, no merging, no worktree removal.
It reports, and prints the commands you would run next.`,
	}
	cmd.AddCommand(newPRListCmd(app), newPRPromptCmd(app))
	return cmd
}

func newPRListCmd(app *App) *cobra.Command {
	opts := prFlags{}
	cmd := &cobra.Command{
		Use:     "list [query]",
		Aliases: []string{"ls"},
		Short:   "List open pull requests you authored or were asked to review",
		Long: `Combine the requests you opened with the ones awaiting your review.

Two provider surfaces are used, because they answer different questions. The
account-wide search is two calls no matter how many repositories you have, but
it cannot report a head branch, so its rows cannot be matched to a worktree.
The per-repository listing does report one, at the cost of a call per
repository, so it is used only for repositories dev is already engaged with
unless --all-repos widens it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = strings.ToLower(args[0])
			}
			collectOptions, err := opts.collectOptions()
			if err != nil {
				return err
			}
			rows, statuses, collectErr := collectPullRequests(ctxOf(), app, collectOptions)
			if len(rows) == 0 && collectErr != nil {
				return collectErr
			}
			rows = filterPRRows(rows, query, opts)

			if opts.JSON {
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(makePRListJSON(rows, statuses, collectOptions)); err != nil {
					return err
				}
			} else {
				renderPRTable(app, rows, statuses, opts.Actions)
			}
			if collectErr != nil {
				app.warnf("partial pull request results: %v", collectErr)
			}
			return nil
		},
	}
	opts.register(cmd)
	return cmd
}

// prFlags are shared by `pr list` and `pr prompt` so that a prompt is rendered
// from exactly the inbox the user just looked at.
type prFlags struct {
	Scope    string
	Roles    []string
	State    string
	Repos    []string
	AllRepos bool
	Linked   bool
	Limit    int
	JSON     bool
	Actions  bool
}

func (f *prFlags) register(cmd *cobra.Command) {
	flags := cmd.Flags()
	flags.StringVar(&f.Scope, "scope", "all", "which surface to query: account, local or all")
	flags.StringSliceVar(&f.Roles, "role", nil, "limit to author or reviewer (default both)")
	flags.StringVar(&f.State, "state", "open", "request state: open, merged, closed or all")
	flags.StringSliceVar(&f.Repos, "repo", nil, "restrict to these owner/name repositories")
	flags.BoolVar(&f.AllRepos, "all-repos", false, "query every discovered repository, not only ones dev has a task for")
	flags.BoolVar(&f.Linked, "linked", false, "only requests with a local checkout")
	flags.IntVar(&f.Limit, "limit", 0, "cap the rows rendered")
	if cmd.Name() != "prompt" {
		flags.BoolVar(&f.JSON, "json", false, "emit structured output")
		flags.BoolVar(&f.Actions, "actions", false, "print the gh/glab commands for each request")
	}
}

func (f *prFlags) collectOptions() (prCollectOptions, error) {
	scope := prScope(strings.ToLower(strings.TrimSpace(f.Scope)))
	switch scope {
	case scopeAccount, scopeLocal, scopeAll:
	default:
		return prCollectOptions{}, fmt.Errorf("unknown --scope %q: want account, local or all", f.Scope)
	}

	state := forge.PRState(strings.ToLower(strings.TrimSpace(f.State)))
	switch state {
	case forge.PRStateOpen, forge.PRStateMerged, forge.PRStateClosed, forge.PRStateAll:
	default:
		return prCollectOptions{}, fmt.Errorf("unknown --state %q: want open, merged, closed or all", f.State)
	}

	var roles []forge.PRRole
	for _, role := range f.Roles {
		switch forge.PRRole(strings.ToLower(strings.TrimSpace(role))) {
		case forge.RoleAuthor:
			roles = append(roles, forge.RoleAuthor)
		case forge.RoleReviewer:
			roles = append(roles, forge.RoleReviewer)
		default:
			return prCollectOptions{}, fmt.Errorf("unknown --role %q: want author or reviewer", role)
		}
	}

	return prCollectOptions{
		Scope:    scope,
		Query:    forge.PRQuery{Roles: roles, State: state},
		Repos:    f.Repos,
		AllRepos: f.AllRepos,
	}, nil
}

func filterPRRows(rows []prRow, query string, opts prFlags) []prRow {
	filtered := make([]prRow, 0, len(rows))
	for _, row := range rows {
		if opts.Linked && row.Local == nil {
			continue
		}
		if query != "" {
			hay := strings.ToLower(strings.Join([]string{
				string(row.PR.Forge), row.PR.Repo, row.PR.Title,
				row.PR.Author, row.PR.HeadBranch,
			}, " "))
			matched := true
			for _, term := range strings.Fields(query) {
				if !strings.Contains(hay, term) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
		}
		filtered = append(filtered, row)
	}
	if opts.Limit > 0 && len(filtered) > opts.Limit {
		filtered = filtered[:opts.Limit]
	}
	return filtered
}

func renderPRTable(app *App, rows []prRow, statuses []prProviderStatus, showActions bool) {
	style := app.outStyle()
	if len(rows) == 0 {
		fmt.Fprintln(app.Out, "No pull requests match.")
		renderPRProviderNotes(app, statuses)
		return
	}
	table := app.newTable("PR", "TITLE", "ROLE", "STATE", "CHECKS", "REVIEW", "LOCAL", "UPDATED")
	for _, row := range rows {
		local := "—"
		if row.Local != nil {
			local = style.success(config.Contract(row.Local.Checkout))
		}
		updated := "—"
		if !row.PR.UpdatedAt.IsZero() {
			updated = row.PR.UpdatedAt.Format("2006-01-02")
		}
		table.Add(
			truncate(prLabel(row.PR), 30),
			truncate(row.PR.Title, 40),
			truncate(prRoleLabel(row.PR), 8),
			prStateLabel(style, row.PR),
			prChecksLabel(style, row.PR),
			truncate(prReviewLabel(style, row.PR), 18),
			truncate(local, 28),
			updated,
		)
	}
	table.Render(app.Out)
	if showActions {
		renderPRActions(app, rows)
	}
	renderPRProviderNotes(app, statuses)
}

// renderPRActions prints the commands for each request. dev never runs them:
// approving and merging are decisions, and a tool that quietly made them would
// be a different and much more dangerous tool.
func renderPRActions(app *App, rows []prRow) {
	style := app.outStyle()
	for _, row := range rows {
		actions := prActions(row)
		if len(actions) == 0 {
			continue
		}
		fmt.Fprintf(app.Out, "\n%s\n", style.label(prLabel(row.PR)))
		names := make([]string, 0, len(actions))
		for name := range actions {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(app.Out, "  %-8s %s\n", name, style.dim(actions[name]))
		}
	}
}

// renderPRProviderNotes explains any provider that contributed nothing, so an
// empty or short list is never mistaken for an empty queue.
func renderPRProviderNotes(app *App, statuses []prProviderStatus) {
	style := app.outStyle()
	for _, status := range statuses {
		if status.Status == string(forge.ReadinessReady) {
			continue
		}
		note := string(status.Forge) + ": " + prProviderPhrase(status.Status)
		if status.Detail != "" {
			note += " — " + status.Detail
		}
		if status.Action != "" {
			note += "; " + status.Action
		}
		fmt.Fprintln(app.Out, style.dim("  "+note))
	}
}

// prProviderPhrase renders a readiness status as something a person reads
// rather than as the enum value.
func prProviderPhrase(status string) string {
	switch forge.ReadinessStatus(status) {
	case forge.ReadinessUnauthenticated:
		return "signed out"
	case forge.ReadinessMissingCLI:
		return "not installed"
	case forge.ReadinessProbeFailed:
		return "could not check sign-in"
	}
	return status
}

func prLabel(pr forge.PullRequest) string {
	return string(pr.Forge) + ":" + pr.Repo + "#" + strconv.Itoa(pr.Number)
}

func prRoleLabel(pr forge.PullRequest) string {
	if len(pr.Roles) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(pr.Roles))
	for _, role := range pr.Roles {
		switch role {
		case forge.RoleAuthor:
			parts = append(parts, "mine")
		case forge.RoleReviewer:
			parts = append(parts, "review")
		}
	}
	return strings.Join(parts, "+")
}

func prStateLabel(style cliStyle, pr forge.PullRequest) string {
	if pr.Draft {
		return style.dim("draft")
	}
	switch pr.State {
	case forge.PRStateMerged:
		return style.success("merged")
	case forge.PRStateClosed:
		return style.dim("closed")
	}
	return "open"
}

func prChecksLabel(style cliStyle, pr forge.PullRequest) string {
	switch pr.Checks {
	case forge.ChecksPassing:
		return style.success("pass")
	case forge.ChecksFailing:
		return style.danger("fail")
	case forge.ChecksPending:
		return style.warning("pending")
	case forge.ChecksNone:
		return "none"
	}
	// Empty means the surface cannot report checks, which is not the same as
	// a request having none.
	return "—"
}

func prReviewLabel(style cliStyle, pr forge.PullRequest) string {
	switch pr.ReviewDecision {
	case "":
		return "—"
	case "approved":
		return style.success("approved")
	case "changes_requested":
		return style.warning("changes")
	case "review_required":
		return "required"
	}
	return pr.ReviewDecision
}

// prListJSON is the structured contract. Fields may be added; existing ones are
// never renamed or removed. It is an object rather than a bare array because a
// consumer needs to distinguish "no requests" from "GitLab was signed out".
type prListJSON struct {
	GeneratedAt  time.Time          `json:"generated_at"`
	Scope        string             `json:"scope"`
	State        string             `json:"state"`
	Providers    []prProviderStatus `json:"providers"`
	PullRequests []prJSONRow        `json:"pull_requests"`
}

type prJSONRow struct {
	forge.PullRequest
	Local   *prLocal          `json:"local,omitempty"`
	Actions map[string]string `json:"actions,omitempty"`
}

func makePRListJSON(rows []prRow, statuses []prProviderStatus, opts prCollectOptions) prListJSON {
	out := prListJSON{
		GeneratedAt:  time.Now().UTC(),
		Scope:        string(opts.Scope),
		State:        string(opts.Query.EffectiveState()),
		Providers:    statuses,
		PullRequests: make([]prJSONRow, 0, len(rows)),
	}
	if out.Scope == "" {
		out.Scope = string(scopeAll)
	}
	for _, row := range rows {
		out.PullRequests = append(out.PullRequests, makePRJSONRow(row))
	}
	return out
}

func makePRJSONRow(row prRow) prJSONRow {
	return prJSONRow{PullRequest: row.PR, Local: row.Local, Actions: prActions(row)}
}

// prActions renders the commands a person or an agent would run next. dev
// prints them and never executes them: approving and merging are decisions,
// not conveniences.
func prActions(row prRow) map[string]string {
	number := strconv.Itoa(row.PR.Number)
	target := " --repo " + row.PR.Repo
	// Reviewing and merging only make sense while a request is open, and
	// offering `gh pr merge` on something already merged is worse than
	// offering nothing.
	open := row.PR.State == forge.PRStateOpen
	actions := map[string]string{}
	switch row.PR.Forge {
	case forge.GitHub:
		actions["view"] = "gh pr view " + number + target + " --web"
		actions["diff"] = "gh pr diff " + number + target
		actions["checks"] = "gh pr checks " + number + target
		if open {
			actions["approve"] = "gh pr review " + number + target + " --approve"
			actions["comment"] = "gh pr comment " + number + target + " --body '...'"
			actions["merge"] = "gh pr merge " + number + target + " --squash"
		}
	case forge.GitLab:
		actions["view"] = "glab mr view " + number + target + " --web"
		actions["diff"] = "glab mr diff " + number + target
		if open {
			actions["approve"] = "glab mr approve " + number + target
			actions["comment"] = "glab mr note " + number + target + " --message '...'"
			actions["merge"] = "glab mr merge " + number + target
		}
	default:
		return nil
	}
	if row.Local != nil && row.Local.TaskID != "" {
		// Only offered once the request is actually merged; dev never infers
		// integration from a forge answer on its own.
		if row.PR.State == forge.PRStateMerged {
			actions["retire"] = "dev sweep --merged-worktrees"
		}
		actions["resume"] = "dev resume " + row.Local.TaskID
	}
	return actions
}
