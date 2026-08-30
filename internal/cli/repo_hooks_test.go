package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
)

func TestProjectAuthoredRemoteSkillSetupFailsClosed(t *testing.T) {
	source := filepath.Join(t.TempDir(), ".dev-cli", "scaffolds.toml")
	app := &App{Cfg: config.Default(), In: bytes.NewBuffer(nil), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	project := projectconfig.Result{
		ScaffoldsPresent: true,
		Paths:            projectconfig.FilePaths{Scaffolds: source},
		ExecutionHash:    "sha256:" + strings.Repeat("a", 64),
	}
	plan := scaffold.Plan{Skills: []scaffold.SkillPlan{{
		Name: "remote-setup", Source: "owner/skills", Origin: source,
		Setup: &scaffold.SetupPlan{Phase: scaffold.BeforeCommit, Script: "scripts/setup.sh"},
	}}}
	err := ensureProjectConfigTrust(app, t.TempDir(), project, plan)
	if err == nil || !strings.Contains(err.Error(), "mutable remote source") {
		t.Fatalf("remote project skill setup should fail closed: %v", err)
	}
}

func TestRunRepoHooksHonorsPhasesAndOptionalFailures(t *testing.T) {
	root := t.TempDir()
	app := &App{In: bytes.NewBuffer(nil), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	hooks := []repoHookSpec{
		{ID: "write", Phase: repoHookBeforeCommit, Command: []string{"sh", "-c", "printf ok > generated.txt"}, Required: true},
		{ID: "later", Phase: repoHookAfterCommit, Command: []string{"sh", "-c", "exit 0"}},
		{ID: "optional", Phase: repoHookBeforeCommit, Command: []string{"sh", "-c", "exit 3"}},
	}
	result, err := runRepoHooks(t.Context(), app, root, repoHookBeforeCommit, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ran) != 1 || len(result.Warnings) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if body, err := os.ReadFile(filepath.Join(root, "generated.txt")); err != nil || string(body) != "ok" {
		t.Fatalf("generated = %q, %v", body, err)
	}
}

func TestValidateRepoWorkflowAllowsGeneratedRepoLocalHookExecutable(t *testing.T) {
	root := t.TempDir()
	app := &App{interactiveCheck: func() bool { return false }}
	request := repoWorkflowRequest{
		Destination: root,
		Prepared: preparedRepoScaffold{Plan: scaffold.Plan{Hooks: []scaffold.HookPlan{{
			ID: "bootstrap", Command: []string{"./scripts/bootstrap.sh"}, Required: true,
		}}}},
	}
	if err := validateRepoWorkflowRequest(app, request, true); err != nil {
		t.Fatalf("repo-local executable should be checked after scaffold materialization: %v", err)
	}
}

func TestValidateRepoWorkflowRejectsShellRunOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only preflight")
	}
	app := &App{interactiveCheck: func() bool { return false }}
	request := repoWorkflowRequest{
		Prepared: preparedRepoScaffold{
			Plan: scaffold.Plan{Hooks: []scaffold.HookPlan{{
				ID: "shell", Run: "echo hello", Required: true,
			}}},
		},
	}
	if err := validateRepoWorkflowRequest(app, request, true); err == nil || !strings.Contains(err.Error(), "unsupported on Windows") {
		t.Fatalf("error = %v", err)
	}
}
