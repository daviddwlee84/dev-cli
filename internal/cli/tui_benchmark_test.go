package cli

import (
	"context"
	"fmt"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

var benchmarkFleetSnapshot fleet.Snapshot

func BenchmarkFleetSnapshotFromRepoRows(b *testing.B) {
	for _, count := range []int{56, 500} {
		b.Run(fmt.Sprintf("repos-%d", count), func(b *testing.B) {
			rows := make([]tui.RepoRow, count)
			for index := range rows {
				rows[index] = tui.RepoRow{Repo: repo.Repo{
					Name: fmt.Sprintf("repo-%04d", index), Path: fmt.Sprintf("/src/repo-%04d", index),
				}}
			}
			b.ReportAllocs()
			for b.Loop() {
				benchmarkFleetSnapshot = fleetSnapshotFromRepoRows(rows, "none")
			}
		})
	}
}

func BenchmarkTUILocalLoadOrchestration(b *testing.B) {
	tracked := make([]*task.Task, 56)
	discovered := make([]repo.Repo, 56)
	taskRows := make([]inventory.Row, 56)
	repoRows := make([]tui.RepoRow, 56)
	for index := range tracked {
		tracked[index] = &task.Task{ID: fmt.Sprintf("task-%04d", index), Repo: fmt.Sprintf("repo-%04d", index)}
		discovered[index] = repo.Repo{Name: fmt.Sprintf("repo-%04d", index), Path: fmt.Sprintf("/src/repo-%04d", index)}
		taskRows[index] = inventory.Row{Task: tracked[index]}
		repoRows[index] = tui.RepoRow{Repo: discovered[index]}
	}
	loader := &tuiLocalLoader{app: &App{}}
	loader.listTasks = func() ([]*task.Task, error) { return tracked, nil }
	loader.loadRuntime = func(context.Context) (tuiRuntimeSnapshot, error) {
		return tuiRuntimeSnapshot{runtime: runtime.None{}}, nil
	}
	loader.discoverRepos = func(context.Context) ([]repo.Repo, error) { return discovered, nil }
	loader.collectTasks = func(context.Context, []*task.Task, tuiRuntimeSnapshot, *inventory.Limiter) ([]inventory.Row, error) {
		return taskRows, nil
	}
	loader.collectRepos = func(context.Context, []*task.Task, []repo.Repo, tuiRuntimeSnapshot, *inventory.Limiter) ([]tui.RepoRow, error) {
		return repoRows, nil
	}
	loader.collectTries = func(context.Context, bool, tuiRuntimeSnapshot) ([]tui.TryRow, error) { return nil, nil }

	b.ReportAllocs()
	for b.Loop() {
		load := loader.Start(context.Background(), tui.LocalLoadRequest{
			TasksGeneration: 1, ReposGeneration: 1, TriesGeneration: 1,
		})
		for range load.Results {
		}
	}
}
