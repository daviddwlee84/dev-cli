package cli

import (
	"context"
	"errors"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/diskusage"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

// prepareRepoRowCollector reads shared inputs once. Admission and the shared
// limiter belong to callers, not to one goroutine per repository.
func prepareRepoRowCollector(ctx context.Context, app *App, rt runtime.Runtime, options repoCollectOptions) (func(context.Context, tui.RepoRow) tui.RepoRow, error) {
	tasks := options.Tasks
	var taskInventoryErr error
	if !options.TasksSet {
		var diagnostics []task.Diagnostic
		var err error
		tasks, diagnostics, err = app.Tasks.ListWithDiagnostics()
		if err != nil {
			return nil, err
		}
		for _, diagnostic := range diagnostics {
			taskInventoryErr = errors.Join(taskInventoryErr, diagnostic)
		}
	}
	byRepo := tasksByRepoPath(tasks)
	sessions, runtimeErr := options.Sessions, options.SessionsErr
	if !options.SessionsSet {
		sessions, runtimeErr = rt.List(ctx)
	}
	notesByRepo := map[string][]*note.Note{}
	if app.Notes != nil {
		allNotes, err := app.Notes.List("")
		if err != nil {
			return nil, err
		}
		for _, n := range allNotes {
			notesByRepo[n.RepositoryID] = append(notesByRepo[n.RepositoryID], n)
		}
	}
	return func(ctx context.Context, row tui.RepoRow) tui.RepoRow {
		r := row.Repo
		row.ObservedAt = time.Now()
		if row.Asset != nil {
			repoNotes := notesByRepo[row.Asset.ID]
			row.NoteCount = len(repoNotes)
			if len(repoNotes) > 0 {
				row.LatestNote = repoNotes[0]
			}
		}
		if r.HasGit {
			if options.DeferTopology {
				row.TopologyPending = true
				row.TopologyErr = errors.New("recovery details not loaded")
			} else {
				row.Topology, row.TopologyErr = gitx.RecoveryTopologyOf(ctx, r.Path)
			}
		}
		row.Tasks = append(row.Tasks, repoTasks(byRepo, r)...)
		row.Context = inventory.CollectRepoContext(ctx, r, row.Tasks, sessions, rt.Name())
		row.Context.TaskErr = taskInventoryErr
		row.Context.RuntimeErr = runtimeErr
		row.LastActivity, row.Worktrees = row.Context.LastActivity, row.Context.WorktreeCount
		if main, ok := row.Context.Main(); ok {
			row.Status = main.Status
			row.GitKnown = main.StatusErr == nil && main.Exists && main.PathErr == nil && !main.Worktree.Prunable
		}
		if !r.Bare {
			row.RemoteForge, row.RemoteName = forge.IdentityFromURL(gitx.RemoteFromConfig(r.CommonDir, "origin"))
		}
		if live := row.Context.Sessions(); len(live) > 0 {
			row.Live, row.Runtime = true, rt.Name()
			row.RuntimeHandle, row.RuntimeStatus = live[0].Handle, live[0].AgentStatus
		}
		row.SizeTarget = repoSizeTarget(r, row.Worktrees)
		if row.Usage == nil {
			row.Usage = cachedRepoUsage(app.Sizes, row.SizeTarget)
		}
		return row
	}, nil
}

// cachedRepoUsage reads the last measurement (expired ones marked Stale) so a
// row shows a size at once; it never scans. The normal post-load measurement
// still replaces expired values. Reading here keeps JSON decoding off the UI
// loop.
func cachedRepoUsage(sizes *diskusage.Manager, target diskusage.Target) *diskusage.Usage {
	if target.Checkout == "" {
		return nil
	}
	for _, result := range sizes.Lookup([]diskusage.Target{target}) {
		if result.Err == nil {
			usage := result.Usage
			return &usage
		}
	}
	return nil
}

// repoSizeTarget derives the disk-usage target for a repository row. The
// presentation cache reuses it so cached rows can show cached sizes at once.
func repoSizeTarget(r repo.Repo, worktrees int) diskusage.Target {
	if r.HasGit {
		return diskusage.FromGit(gitx.Repo{
			Root: r.Path, GitDir: r.GitDir, GitCommonDir: r.CommonDir,
			MainRoot: r.MainRoot, Name: r.Name, Bare: r.Bare,
		}, worktrees)
	}
	return diskusage.Plain(r.Path)
}

// tasksByRepoPath groups recorded tasks by their repository path.
func tasksByRepoPath(tasks []*task.Task) map[string][]*task.Task {
	byRepo := map[string][]*task.Task{}
	for _, tracked := range tasks {
		byRepo[tracked.RepoPath] = append(byRepo[tracked.RepoPath], tracked)
	}
	return byRepo
}

// repoTasks returns the tasks recorded against a repository's path or real path.
func repoTasks(byRepo map[string][]*task.Task, r repo.Repo) []*task.Task {
	out := append([]*task.Task(nil), byRepo[r.Path]...)
	if r.RealPath != "" && r.RealPath != r.Path {
		out = append(out, byRepo[r.RealPath]...)
	}
	return out
}
