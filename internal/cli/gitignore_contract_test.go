package cli_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitignoreAgentArtifactContract(t *testing.T) {
	h := newHarness(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(h.repo.Root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	h.mustRun("gitignore", "go", "--offline")

	tests := []struct {
		name        string
		path        string
		wantIgnored bool
	}{
		{name: "worktree state", path: ".claude/worktrees/task/file", wantIgnored: true},
		{name: "local Claude settings", path: ".claude/settings.local.json", wantIgnored: true},
		{name: "aider state", path: ".aider.conf.yml", wantIgnored: true},
		{name: "generated Cursor rules", path: ".cursor/rules/_generated/rule.md", wantIgnored: true},
		{name: "SpecStory history", path: ".specstory/history/session.md"},
		{name: "Claude plan", path: ".claude/plans/plan.md"},
		{name: "Cursor plan", path: ".cursor/plans/plan.md"},
		{name: "OpenCode plan", path: ".opencode/plans/plan.md"},
		{name: "Specify artifact", path: ".specify/spec.md"},
		{name: "Codex artifact", path: ".codex/plan.md"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("git", "check-ignore", "--no-index", "-q", "--", tc.path)
			cmd.Dir = h.repo.Root
			output, err := cmd.CombinedOutput()
			ignored := err == nil
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
					t.Fatalf("git check-ignore %s: %v\n%s", filepath.ToSlash(tc.path), err, output)
				}
			}
			if ignored != tc.wantIgnored {
				t.Errorf("git considers %s ignored = %v, want %v", filepath.ToSlash(tc.path), ignored, tc.wantIgnored)
			}
		})
	}
}
