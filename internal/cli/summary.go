package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/summary"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newSummaryCmd(app *App) *cobra.Command {
	var (
		detail         string
		recentCommits  int
		attention      bool
		includeHistory bool
		sizes          bool
		jsonOut        bool
	)
	cmd := &cobra.Command{
		Use:   "summary [query]",
		Short: "Generate an agent-ready snapshot of this machine's projects",
		Long: `Summarize the current repositories, worktrees, tasks, runtimes and Tries
on this machine as Markdown or stable JSON.

This is a current-state snapshot, not a history report. Use dev journal for a
date range and dev repo context for a deep dive into one repository. dev only
generates context; it does not invoke an AI agent or persist the rendered text.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsedDetail := summary.Detail(strings.ToLower(detail))
			switch parsedDetail {
			case summary.DetailAuto, summary.DetailCompact, summary.DetailFull:
			default:
				return fmt.Errorf("unknown --detail %q: want auto, compact or full", detail)
			}
			if recentCommits < 0 {
				return fmt.Errorf("--recent-commits must be zero or positive")
			}
			query := ""
			if len(args) == 1 {
				query = args[0]
			}

			ctx := ctxOf()
			rt := app.Runtime()
			var sessions []runtime.Session
			var warnings []string
			runtimeCollected := !app.noRuntime
			if !app.noRuntime {
				var err error
				sessions, err = rt.List(ctx)
				if err != nil {
					warnings = append(warnings, "runtime: "+err.Error())
					runtimeCollected = false
				}
			}

			repoRows, err := collectReposWithOptions(ctx, app, rt, repoCollectOptions{
				Sessions: sessions, SessionsSet: true,
			})
			if err != nil {
				return err
			}
			tryOptions := experiment.ListOptions{IncludeDeprecated: true}
			if includeHistory {
				tryOptions = experiment.ListOptions{All: true}
			}
			tryRows, tryErr := collectTriesWithOptions(ctx, app, rt, tryOptions, sessions, true)
			if tryErr != nil {
				warnings = append(warnings, "tries: "+tryErr.Error())
			}
			seenPaths := map[string]bool{}
			for _, row := range repoRows {
				seenPaths[cleanPathKey(row.Repo.Path)] = true
				seenPaths[cleanPathKey(row.Repo.RealPath)] = true
			}
			dedupedTries := tryRows[:0]
			for _, row := range tryRows {
				if seenPaths[cleanPathKey(row.Item.Live.CurrentPath)] || seenPaths[cleanPathKey(row.Item.Live.RealPath)] {
					continue
				}
				dedupedTries = append(dedupedTries, row)
			}
			tryRows = dedupedTries

			// Select from cheap current facts before optional size scans or
			// multi-commit history queries.
			var skeletons []summary.Project
			for _, row := range repoRows {
				skeletons = append(skeletons, summaryRepoProject(ctx, row, 0))
			}
			for _, row := range tryRows {
				skeletons = append(skeletons, summaryTryProject(ctx, row, 0))
			}
			selection := summary.Build(config.Hostname(), skeletons, summary.Capabilities{}, nil,
				summary.Options{Query: query, Attention: attention})
			selected := map[string]bool{}
			for _, project := range selection.Projects {
				selected[summaryProjectKey(project)] = true
			}
			repoRows = filterSummaryRepoRows(repoRows, selected)
			tryRows = filterSummaryTryRows(tryRows, selected)

			if sizes {
				measureRepoRows(ctx, app, repoRows, false)
				items := make([]experiment.Item, len(tryRows))
				for i := range tryRows {
					items[i] = tryRows[i].Item
				}
				measurements := measureTryItems(ctx, app, items, false)
				for i := range tryRows {
					measurement := measurements[tryRows[i].Item.ID]
					tryRows[i].Usage, tryRows[i].SizeError = measurement.Usage, measurement.Err
				}
			}

			projects := make([]summary.Project, 0, len(repoRows)+len(tryRows))
			for _, row := range repoRows {
				projects = append(projects, summaryRepoProject(ctx, row, recentCommits))
			}
			for _, row := range tryRows {
				projects = append(projects, summaryTryProject(ctx, row, recentCommits))
			}
			report := summary.Build(config.Hostname(), projects, summary.Capabilities{
				RuntimeCollected: runtimeCollected, SizesCollected: sizes,
			}, warnings, summary.Options{})
			if jsonOut {
				enc := json.NewEncoder(app.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			var doc bytes.Buffer
			summary.RenderMarkdown(&doc, report, parsedDetail, attention)
			fmt.Fprint(app.Out, renderMarkdown(doc.String(), app.outStyle()))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&detail, "detail", "auto", "Markdown detail: auto, compact or full")
	f.IntVar(&recentCommits, "recent-commits", 1, "recent commits per Git project (0 to omit)")
	f.BoolVar(&attention, "attention", false, "only projects with active work or recovery risk")
	f.BoolVar(&includeHistory, "include-history", false, "include archived, evicted and graduated Try history")
	f.BoolVar(&sizes, "sizes", false, "include cached or measured logical disk usage")
	f.BoolVar(&jsonOut, "json", false, "emit the complete stable JSON snapshot")
	registerFlagCompletion(cmd, "detail", fixedCompletions("auto", "compact", "full"))
	return cmd
}

func summaryRepoProject(ctx context.Context, row tui.RepoRow, recent int) summary.Project {
	project := summary.Project{
		Kind: "repository", Name: row.Repo.Display(), Category: row.Repo.Category,
		Path: row.Repo.Path, DisplayPath: config.Contract(row.Repo.Path), Present: true,
		LatestActivity: row.LastActivity, Size: row.Usage,
	}
	if row.SizeError != nil {
		project.SizeError = row.SizeError.Error()
	}
	if row.Asset != nil {
		project.ID = row.Asset.ID
		project.Tags = append([]string(nil), row.Asset.Tags...)
		project.Note = row.Asset.Note
	}
	project.Git = summaryGit(row.Status)
	project.Recovery = summaryRecovery(row.Topology, row.TopologyErr)
	for _, checkout := range row.Context.Checkouts {
		item := summary.Checkout{
			Path: checkout.Worktree.Path, DisplayPath: config.Contract(checkout.Worktree.Path),
			Branch: checkout.Branch(), Ownership: string(checkout.Ownership), Exists: checkout.Exists,
			Prunable: checkout.Worktree.Prunable, Locked: checkout.Worktree.Locked,
		}
		if checkout.StatusErr == nil && checkout.Exists && !checkout.Worktree.Bare {
			item.Git = summaryGit(checkout.Status)
		}
		if !checkout.LastCommit.IsZero() || checkout.LastSubject != "" {
			item.LastCommit = &summary.Commit{Subject: checkout.LastSubject, AuthoredAt: checkout.LastCommit}
		}
		project.Checkouts = append(project.Checkouts, item)
	}
	project.Tasks = summaryTasks(row.Tasks)
	project.Sessions = summarySessions(row.Context.Runtime, row.Context.Sessions())
	project.RecentCommits = summaryRecentRepoCommits(ctx, row, recent)
	return project
}

func summaryTryProject(ctx context.Context, row tui.TryRow, recent int) summary.Project {
	item := row.Item
	project := summary.Project{
		Kind: "try", ID: item.ID, Name: item.DisplayName(), Phase: string(item.Phase),
		Path: item.Live.CurrentPath, DisplayPath: config.Contract(item.Live.CurrentPath),
		Present: item.Live.Present, Tags: append([]string(nil), item.Tags...), Note: item.Note,
		LatestActivity: item.Activity(), Size: row.Usage,
	}
	if row.SizeError != nil {
		project.SizeError = row.SizeError.Error()
	}
	if item.Live.Status != nil {
		project.Git = summaryGit(*item.Live.Status)
		project.Recovery = summaryRecovery(row.Topology, row.TopologyErr)
		checkout := summary.Checkout{
			Path: item.Live.CurrentPath, DisplayPath: config.Contract(item.Live.CurrentPath),
			Branch: item.Live.Status.Branch, Ownership: "try", Exists: item.Live.Present,
			Git: summaryGit(*item.Live.Status),
		}
		if !item.Live.LastCommit.IsZero() || item.Live.LastCommitSubject != "" {
			checkout.LastCommit = &summary.Commit{Subject: item.Live.LastCommitSubject, AuthoredAt: item.Live.LastCommit}
		}
		project.Checkouts = []summary.Checkout{checkout}
	}
	if row.Live {
		project.Sessions = []summary.Session{{Runtime: row.Runtime, Handle: row.RuntimeHandle, Status: row.RuntimeStatus}}
	}
	if recent > 0 && item.Live.Repo != nil {
		if recent == 1 && item.Live.LastCommitSubject != "" {
			project.RecentCommits = []summary.Commit{{Subject: item.Live.LastCommitSubject, AuthoredAt: item.Live.LastCommit}}
		} else {
			project.RecentCommits = recentGitCommits(ctx, item.Live.CurrentPath, recent)
		}
	}
	return project
}

func summaryRecentRepoCommits(ctx context.Context, row tui.RepoRow, recent int) []summary.Commit {
	if recent <= 0 || !row.Repo.HasGit {
		return nil
	}
	if recent > 1 {
		return recentGitCommits(ctx, row.Repo.Path, recent)
	}
	var commits []summary.Commit
	for _, checkout := range row.Context.Checkouts {
		if checkout.LastSubject != "" {
			commits = append(commits, summary.Commit{Subject: checkout.LastSubject, AuthoredAt: checkout.LastCommit})
		}
	}
	sort.SliceStable(commits, func(i, j int) bool { return commits[i].AuthoredAt.After(commits[j].AuthoredAt) })
	if len(commits) > 1 {
		commits = commits[:1]
	}
	return commits
}

func recentGitCommits(ctx context.Context, path string, limit int) []summary.Commit {
	if limit <= 0 || path == "" {
		return nil
	}
	out, err := gitx.Run(ctx, path, "log", "--branches", "--remotes", "--source",
		fmt.Sprintf("-%d", limit), "--format=%H%x1f%aI%x1f%S%x1f%s")
	if err != nil {
		return nil
	}
	var commits []summary.Commit
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\x1f", 4)
		if len(fields) != 4 {
			continue
		}
		when, _ := time.Parse(time.RFC3339, fields[1])
		commits = append(commits, summary.Commit{
			OID: fields[0], ShortOID: summaryShortOID(fields[0]), AuthoredAt: when,
			Source: strings.TrimPrefix(strings.TrimPrefix(fields[2], "refs/heads/"), "refs/remotes/"), Subject: fields[3],
		})
	}
	return commits
}

func summaryGit(status gitx.Status) *summary.Git {
	return &summary.Git{
		Branch: status.Branch, Detached: status.Detached, Dirty: status.Dirty(),
		Conflicted: status.Conflicted, Changed: status.Changed, Staged: status.Staged,
		Unstaged: status.Unstaged, Untracked: status.Untracked, Ahead: status.Ahead,
		Behind: status.Behind, Upstream: status.Upstream, Summary: status.Summary(),
	}
}

func summaryRecovery(topology gitx.RecoveryTopology, err error) *summary.Recovery {
	result := &summary.Recovery{
		LocalOnlyBranches: append([]string(nil), topology.LocalOnlyBranches...),
		UpstreamRemotes:   append([]string(nil), topology.UpstreamRemotes...),
		NoRemote:          err == nil && !topology.HasRemote(), MultipleRemotes: topology.MultipleRemotes(),
		MultipleUpstreams: topology.MultipleUpstreams(),
	}
	for _, remote := range topology.Remotes {
		result.Remotes = append(result.Remotes, remote.Name)
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func summaryTasks(tasks []*task.Task) []summary.Task {
	seen := map[string]bool{}
	var out []summary.Task
	for _, item := range tasks {
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		out = append(out, summary.Task{
			ID: item.ID, Title: item.Title(), State: string(item.State), Branch: item.Branch,
			Next: item.Next, Note: item.Note, Tags: append([]string(nil), item.Tags...),
		})
	}
	return out
}

func summarySessions(runtimeName string, sessions []runtime.Session) []summary.Session {
	var out []summary.Session
	for _, session := range sessions {
		out = append(out, summary.Session{
			Runtime: runtimeName, Handle: session.Handle, Status: session.AgentStatus,
			AgentSessions: append([]string(nil), session.AgentSessions...),
		})
	}
	return out
}

func cleanPathKey(path string) string {
	if path == "" {
		return ""
	}
	return strings.TrimRight(path, "/")
}

func summaryShortOID(oid string) string {
	if len(oid) > 10 {
		return oid[:10]
	}
	return oid
}

func summaryProjectKey(project summary.Project) string {
	identity := project.Path
	if identity == "" {
		identity = project.ID
	}
	return project.Kind + "\x00" + cleanPathKey(identity)
}

func filterSummaryRepoRows(rows []tui.RepoRow, selected map[string]bool) []tui.RepoRow {
	out := rows[:0]
	for _, row := range rows {
		project := summary.Project{Kind: "repository", Path: row.Repo.Path, ID: ""}
		if selected[summaryProjectKey(project)] {
			out = append(out, row)
		}
	}
	return out
}

func filterSummaryTryRows(rows []tui.TryRow, selected map[string]bool) []tui.TryRow {
	out := rows[:0]
	for _, row := range rows {
		project := summary.Project{Kind: "try", Path: row.Item.Live.CurrentPath, ID: row.Item.ID}
		if selected[summaryProjectKey(project)] {
			out = append(out, row)
		}
	}
	return out
}
