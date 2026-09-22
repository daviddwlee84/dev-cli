package cli

import (
	"context"
	"os/exec"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/desktop"
	"github.com/daviddwlee84/dev-cli/internal/snippet"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func newTUISnippetActions(current func() *App) tui.SnippetActions {
	return tui.SnippetActions{
		Load: func(ctx context.Context, query tui.SnippetQuery) (tui.SnippetResult, error) {
			result, err := snippetServiceFor(current()).List(ctx, snippet.ListOptions{
				Forge: snippet.Kind(query.Provider), Project: query.Project, Query: query.Text, Content: query.Content,
			})
			out := tui.SnippetResult{Complete: result.Complete}
			if result.Items != nil {
				out.Rows = make([]tui.SnippetRow, 0, len(result.Items))
			}
			for _, item := range result.Items {
				row := tui.SnippetRow{
					Key:      item.Identity.Key(),
					Provider: string(item.Forge), Host: item.Host, ID: item.ID, Project: item.Project,
					Title: item.Title, Description: item.Description, Owner: item.Owner, URL: item.URL,
					Visibility: item.Visibility, UpdatedAt: item.UpdatedAt,
					NodeID: item.NodeID, ProjectID: item.ProjectID, FilesComplete: item.FilesComplete, Metrics: item.Metrics,
				}
				for _, file := range item.Files {
					row.Files = append(row.Files, file.Name)
				}
				out.Rows = append(out.Rows, row)
			}
			var warnings []string
			for _, issue := range result.Issues {
				warnings = append(warnings, string(issue.Forge)+": "+issue.Message)
			}
			out.Warning = strings.Join(warnings, "; ")
			return out, err
		},
		LoadMetrics: func(ctx context.Context, rows []tui.SnippetRow, emit func([]tui.SnippetRow)) error {
			items := make([]snippet.Item, 0, len(rows))
			original := map[string]tui.SnippetRow{}
			for _, row := range rows {
				item := snippetMetricItem(row)
				items = append(items, item)
				original[item.Identity.Key()] = row
			}
			return snippetServiceFor(current()).RefreshMetrics(ctx, items, func(patches []snippet.Item) {
				if ctx.Err() != nil {
					return
				}
				var out []tui.SnippetRow
				for _, item := range patches {
					if row, ok := original[item.Identity.Key()]; ok {
						row.Metrics = item.Metrics
						out = append(out, row)
					}
				}
				emit(out)
			})
		},
		Open: func(ctx context.Context, row tui.SnippetRow) error { return desktop.OpenURL(ctx, row.URL) },
		Create: func(query tui.SnippetQuery) (*exec.Cmd, error) {
			args := []string{"create"}
			if query.Provider != "" && query.Provider != "all" {
				args = append(args, "--forge", query.Provider)
			}
			if query.Project != "" {
				args = append(args, "--project", query.Project)
			}
			return snippetCLIProcess(current(), args...)
		},
	}
}
