package cli

import (
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

func TestTUIWorktreeStartUsesNativeLabelAndNoNoneHandle(t *testing.T) {
	row := tui.RepoRow{Repo: repo.Repo{Name: "demo", Path: "/repo", Category: "Apps"}}
	req := tuiStartRequest(row, "feat/auth", "main")
	if req.Label != "demo/feat/auth" {
		t.Fatalf("TUI worktree label = %q", req.Label)
	}

	created := &wt.CreateResult{
		Path:    "/worktrees/demo/feat-auth",
		Runtime: runtime.OpenResult{Handle: "/worktrees/demo/feat-auth"},
	}
	tk := tuiStartedTask(row, "auth", "feat/auth", "main", created, runtime.None{})
	if tk.RuntimeHandle != "" || tk.RuntimeName != "" {
		t.Fatalf("TUI persisted None pseudo-handle: %+v", tk)
	}
}
