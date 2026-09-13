package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

func TestHygieneCLIWriterGuardProtectsArtifactPathAliases(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	repo := gittest.New(t)
	backend := &currentPaneRuntime{activityRuntime: &activityRuntime{name: "herdr", activities: []runtime.AgentActivity{{PaneID: "w1:p1", WorkspaceID: "w1", Agent: "codex", Status: "idle", CWD: repo.Root}}}, currentPane: "w1:p1"}
	cli := &hygieneCLI{app: &App{runtimeInstance: backend}}
	// The caller's ordinary checkout edits are allowed by the occupancy service;
	// artifact edits still have to protect the caller's active recorder.
	if err := cli.guard(t.Context(), repo.Root, []string{filepath.Join(repo.Root, "notes.md")}); err != nil {
		t.Fatalf("ordinary caller edit blocked: %v", err)
	}
	for _, name := range []string{".SPECSTORY/history/session.md", ".CoDeX/session.jsonl", ".claude./plans/task.md", ".specstory /history/session.md", "member/.OpenCode/plan.md"} {
		path := filepath.Join(repo.Root, filepath.FromSlash(name))
		if err := cli.guard(t.Context(), repo.Root, []string{path}); err == nil || !strings.Contains(err.Error(), "recognized agent still occupies") {
			t.Errorf("artifact alias bypassed occupancy: %q %v", name, err)
		}
	}
}
