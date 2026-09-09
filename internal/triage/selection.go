package triage

import (
	"reflect"
	"sort"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// Target pins a dashboard selection. A repository includes every local branch
// and registered worktree; a Try remains a separately identified directory.
type Target struct {
	Path         string
	RepositoryID string
	CatalogID    string
	Kind         string
}

func sameSnapshotTasks(snapshot inventory.RepoContext, tasks []*task.Task, worktrees []gitx.Worktree) bool {
	if snapshot.TaskErr != nil || snapshot.IdentityErr != nil || len(snapshot.Checkouts) != len(worktrees) {
		return false
	}
	old := []string{}
	for n, row := range snapshot.Checkouts {
		if !reflect.DeepEqual(row.Worktree, worktrees[n]) {
			return false
		}
		for _, t := range row.Tasks {
			old = append(old, t.ID+":"+t.Revision())
		}
	}
	for _, t := range snapshot.OtherTasks {
		old = append(old, t.ID+":"+t.Revision())
	}
	now := []string{}
	for _, t := range tasks {
		now = append(now, t.ID+":"+t.Revision())
	}
	sort.Strings(old)
	sort.Strings(now)
	return reflect.DeepEqual(old, now)
}

// RepositorySnapshot is a same-process observation, never Apply authority.
// It reuses the dashboard's metadata and task/worktree binding, including errors.
type RepositorySnapshot struct {
	Repo        repo.Repo
	Context     inventory.RepoContext
	Topology    gitx.RecoveryTopology
	TopologyErr error
	Asset       *catalog.Entry
	ObservedAt  time.Time
}
