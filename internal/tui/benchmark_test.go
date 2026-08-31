package tui

import (
	"fmt"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func BenchmarkInitialView(b *testing.B) {
	model := New(Actions{}, nil, nil).BeginLoading()
	b.ReportAllocs()
	for b.Loop() {
		_ = model.View()
	}
}

func BenchmarkTaskView(b *testing.B) {
	for _, count := range []int{56, 200, 500} {
		b.Run(fmt.Sprintf("rows-%d", count), func(b *testing.B) {
			rows := make([]inventory.Row, count)
			for index := range rows {
				rows[index] = inventory.Row{
					Task: &task.Task{
						ID: fmt.Sprintf("task-%04d", index), Name: fmt.Sprintf("task-%04d", index),
						Repo: "repo", Branch: "main", State: task.Hot,
					},
					CheckoutExists: true,
				}
			}
			model := New(Actions{}, rows, nil)
			b.ReportAllocs()
			for b.Loop() {
				_ = model.View()
			}
		})
	}
}

func BenchmarkDiscardedLocalGeneration(b *testing.B) {
	model := New(Actions{}, nil, nil).BeginLoading()
	stale := LocalResult{View: ViewRepos, Generation: 0, Valid: true}
	b.ReportAllocs()
	for b.Loop() {
		candidate := model
		candidate, _ = candidate.applyLocalResult(stale)
	}
}
