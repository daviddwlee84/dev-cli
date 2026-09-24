package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/desktop"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
	"github.com/daviddwlee84/dev-cli/internal/prflow"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newTUIPRActions(current func() *App) tui.PRActions {
	settings := current().Cfg.EffectiveRemotePRs()
	loads := make(chan struct{}, 2)
	return tui.PRActions{
		PageSize: settings.PageSize, LargeRepoThreshold: settings.LargeRepoThreshold, TTL: settings.CacheTTL.Duration,
		Load: func(ctx context.Context, query tui.PRQuery) (tui.PRResult, error) {
			select {
			case loads <- struct{}{}:
				defer func() { <-loads }()
			case <-ctx.Done():
				return tui.PRResult{}, ctx.Err()
			}
			app := current()
			web, ok := forge.DeriveWebURL(forge.WebURLRequest{Exact: &query.Repository})
			if !ok {
				return tui.PRResult{}, errors.New("repository has no supported PR identity")
			}
			ref, err := prRepoReference(web.URL)
			if err != nil {
				return tui.PRResult{}, err
			}
			service := prService(app, ref)
			scope := query.Scope
			var overall *int
			lowerBound := false
			if metrics := query.Repository.Metrics; metrics != nil && forgemetrics.Fresh(metrics.OpenPRs, app.Cfg.Forge.CacheTTL.Duration, time.Now()) && metrics.OpenPRs.Value != nil {
				n := int(*metrics.OpenPRs.Value)
				overall = &n
			}
			threshold := app.Cfg.EffectiveRemotePRs().LargeRepoThreshold
			if scope == tui.PRScopeAuto {
				scope = tui.PRScopeAll
				if overall != nil && *overall > threshold {
					scope = tui.PRScopeRelated
				}
			}
			page, err := service.ListPage(ctx, prflow.Query{Reference: ref, Relationship: string(scope), State: forge.PRStateOpen, Limit: query.Limit, Cursor: query.Cursor})
			if scope == tui.PRScopeAll && page.Total != nil {
				overall = page.Total
				lowerBound = page.TotalLowerBound
			}
			if query.Scope == tui.PRScopeAuto && scope == tui.PRScopeAll && overall != nil && *overall > threshold && err == nil {
				scope = tui.PRScopeRelated
				page, err = service.ListPage(ctx, prflow.Query{Reference: ref, Relationship: string(scope), State: forge.PRStateOpen, Limit: query.Limit})
			}
			result := tui.PRResult{Scope: scope, Total: overall, TotalLowerBound: lowerBound, HasMore: page.NextCursor != "", NextCursor: page.NextCursor, ObservedAt: time.Now()}
			if page.PullRequests != nil {
				result.Rows = make([]tui.PRRow, 0, len(page.PullRequests))
			}
			for _, request := range page.PullRequests {
				result.Rows = append(result.Rows, tui.PRRow{PR: request, LocalPath: prSnapshotCheckout(query.Locals, request)})
			}
			if !page.Complete && page.NextCursor == "" {
				result.Warning = "Provider returned partial coverage; refresh to retry."
			}
			return result, err
		},
		Run: func(ctx context.Context, request tui.PRActionRequest) (tui.Workflow, error) {
			ref, err := prflow.ParseReference(request.Row.PR.URL)
			if err != nil {
				return nil, err
			}
			if ref.Forge != request.Repository.Forge || ref.Repo != request.Repository.FullName || ref.Number != request.Row.PR.Number || !strings.EqualFold(ref.Host, request.Row.PR.Host) {
				return nil, errors.New("selected PR no longer matches its repository")
			}
			workflow := &prTUIWorkflow{ctx: ctx, app: *current(), request: request}
			workflow.app.workflowHandoff = func(handoff func() error) error { workflow.result.AfterExit = handoff; return nil }
			return workflow, nil
		},
	}
}

// This projection is advisory and uses only the accepted local snapshot. All
// actions independently resolve exact checkout identity under their own guards.
func prSnapshotCheckout(rows []tui.RepoRow, request forge.PullRequest) string {
	headRepo := request.HeadRepo
	if headRepo == "" && !request.CrossRepository {
		headRepo = request.Repo
	}
	if headRepo == "" || request.HeadBranch == "" {
		return ""
	}
	paths := map[string]bool{}
	for _, row := range rows {
		if row.Pending != "" || row.TopologyPending || row.TopologyErr != nil || row.Context.IdentityErr != nil || row.Context.WorktreeErr != nil {
			continue
		}
		matching := map[string]bool{}
		for _, remote := range row.Topology.Remotes {
			if len(remote.FetchURLs) != 1 {
				continue
			}
			identity := forge.ParseRemoteIdentity(remote.FetchURLs[0])
			if identity.Kind == request.Forge && strings.EqualFold(identity.Host, request.Host) && strings.EqualFold(identity.Name, headRepo) {
				matching[remote.Name] = true
			}
		}
		for _, checkout := range row.Context.Checkouts {
			if !checkout.Exists || checkout.PathErr != nil || checkout.IdentityErr != nil || checkout.StatusErr != nil || checkout.Worktree.Prunable || checkout.Worktree.Locked || checkout.Worktree.Detached || checkout.Branch() != request.HeadBranch || checkout.Status.Branch != request.HeadBranch {
				continue
			}
			for _, branch := range row.Topology.Branches {
				if branch.Branch == request.HeadBranch && matching[branch.Remote] && branch.Upstream == branch.Remote+"/"+request.HeadBranch {
					paths[checkout.Worktree.Path] = true
				}
			}
		}
	}
	if len(paths) == 1 {
		for path := range paths {
			return path
		}
	}
	return ""
}

type prTUIWorkflow struct {
	ctx     context.Context
	app     App
	request tui.PRActionRequest
	result  tui.WorkflowResult
}

func (w *prTUIWorkflow) SetStdin(v io.Reader)       { w.app.In = v }
func (w *prTUIWorkflow) SetStdout(v io.Writer)      { w.app.Out = v }
func (w *prTUIWorkflow) SetStderr(v io.Writer)      { w.app.Err = v }
func (w *prTUIWorkflow) Result() tui.WorkflowResult { return w.result }

func (w *prTUIWorkflow) Run() error {
	err := w.run()
	if errors.Is(err, errPromptCanceled) {
		w.result.Status = "canceled"
		return nil
	}
	return err
}

func (w *prTUIWorkflow) run() error {
	app, request := &w.app, w.request
	ref, err := prflow.ParseReference(request.Row.PR.URL)
	if err != nil {
		return err
	}
	if request.Action == tui.PRBrowser {
		return desktop.OpenURL(w.ctx, ref.URL())
	}
	if request.Action == tui.PRView {
		detail, err := prService(app, ref).Detail(w.ctx, ref)
		if err != nil {
			return err
		}
		renderPRDetail(app, detail)
		row := request.Row
		row.PR, row.HeadOID, row.Readiness = detail.PullRequest, detail.HeadOID, detail.Readiness
		row.ObservedAt, row.Stale, row.LocalPath = detail.ObservedAt, false, ""
		row.ChangedFiles, row.Additions, row.Deletions = detail.Size.Files, detail.Size.Additions, detail.Size.Deletions
		if localService, localErr := prCheckoutService(w.ctx, app, ref, ""); localErr == nil {
			if locals, localErr := localService.Local(w.ctx, detail); localErr == nil {
				row.LocalPath = ""
				for _, local := range locals {
					if local.Path != "" {
						if row.LocalPath != "" && row.LocalPath != local.Path {
							row.LocalPath = ""
							break
						}
						row.LocalPath = local.Path
					}
				}
			}
		}
		w.result.PR, w.result.Status = &row, "request status refreshed"
		if app.interactive() {
			_, err = newPrompter(app).line("Press Enter to return", "")
		}
		return err
	}
	var cmd *cobra.Command
	args := []string{ref.URL()}
	switch request.Action {
	case tui.PRDiff:
		if _, err := exec.LookPath("diffnav"); err != nil {
			return errors.New("diffnav is unavailable; use Open in browser or dev pr diff --no-pager")
		}
		cmd = newPRDiffCmd(app)
	case tui.PRCheckout, tui.PRTry:
		result, err := runPRCheckout(w.ctx, app, ref, prCheckoutFlags{Try: request.Action == tui.PRTry})
		w.result.RefreshRepos = true
		if err == nil {
			row := request.Row
			row.LocalPath = result.Path
			row.HeadOID = result.HeadOID
			row.Stale = true
			w.result.PR = &row
			w.result.Status = "PR checkout ready"
		}
		return err
	case tui.PRProvision:
		return w.provision(ref)
	case tui.PRMerge, tui.PRMergeSync:
		cmd = newPRMergeCmd(app)
		args = append(args, "--squash")
		if request.Action == tui.PRMergeSync {
			args = append(args, "--sync-base", "ff-only")
		}
	case tui.PRSyncBase:
		cmd = newPRSyncBaseCmd(app)
		if app.interactive() {
			strategy, err := newPrompter(app).choiceOf("Base synchronization strategy", "ff-only", []string{"ff-only", "rebase", "cancel"}, map[string]string{"ff-only": "ff-only", "rebase": "rebase", "cancel": "cancel"})
			if err != nil {
				return err
			}
			if strategy == "cancel" {
				return errPromptCanceled
			}
			args = append(args, "--strategy", strategy)
		}
	default:
		return fmt.Errorf("unknown PR action %q", request.Action)
	}
	cmd.SetContext(w.ctx)
	if err := cmd.ParseFlags(args); err != nil {
		return err
	}
	args = cmd.Flags().Args()
	if cmd.Args != nil {
		if err := cmd.Args(cmd, args); err != nil {
			return err
		}
	}
	err = cmd.RunE(cmd, args)
	w.result.RefreshRepos = request.Action != tui.PRDiff
	if err == nil {
		w.result.Status = "returned from PR " + string(request.Action)
	}
	return err
}
