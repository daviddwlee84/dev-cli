package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/repocontext"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestRepoContextJSONPreservesCorruptTaskAsIncompleteInventory(t *testing.T) {
	repository := gittest.New(t)
	t.Chdir(repository.Root)
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "broken.toml"), []byte("not = valid = toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	remotesPath := filepath.Join(root, "remotes.toml")
	if err := os.WriteFile(remotesPath, []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := &App{
		Cfg: config.Default(), Tasks: task.NewStore(tasksDir), Out: &stdout, Err: &stderr,
		remotesPath: remotesPath, runtimeInstance: &activityRuntime{name: "none"},
	}
	cmd := newRepoContextCmd(app)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("repo context --json: %v\nstderr: %s", err, stderr.String())
	}
	var report repocontext.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Local.TaskInventoryComplete {
		t.Fatal("corrupt task record was reported as a complete inventory")
	}
	found := false
	for _, collectionErr := range report.Errors {
		if collectionErr.Code == "task-inventory-failed" && strings.Contains(collectionErr.Message, "broken.toml") {
			found = true
		}
	}
	if !found {
		t.Fatalf("task inventory diagnostic missing: %+v", report.Errors)
	}
}

func TestRepoContextExplicitLinkedPathKeepsCanonicalCloneAndTasks(t *testing.T) {
	repository := gittest.New(t)
	linked := filepath.Join(t.TempDir(), "linked")
	if err := gitx.AddWorktree(t.Context(), repository.Root, linked, "feat/context", "main"); err != nil {
		t.Fatal(err)
	}
	store := task.NewStore(t.TempDir())
	tracked := &task.Task{
		Name: "main task", Repo: "repo", RepoPath: repository.Root,
		Branch: "main", Base: "main", Mode: task.ModeDirect, State: task.Warm,
	}
	if err := store.Save(tracked); err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: config.Default(), Tasks: store, runtimeInstance: &activityRuntime{name: "none"}}
	resolved, selected, err := resolveRepoContextTarget(t.Context(), app, []string{linked})
	if err != nil {
		t.Fatal(err)
	}
	mainReal, _ := filepath.EvalSymlinks(repository.Root)
	resolvedReal, _ := filepath.EvalSymlinks(resolved.Path)
	selectedReal, _ := filepath.EvalSymlinks(selected)
	linkedReal, _ := filepath.EvalSymlinks(linked)
	if resolvedReal != mainReal || selectedReal != linkedReal {
		t.Fatalf("resolved main=%q selected=%q, want main=%q linked=%q", resolvedReal, selectedReal, mainReal, linkedReal)
	}
	local := collectLocalRepoContext(t.Context(), app, resolved, selected, false)
	if len(local.Context.Checkouts) != 2 || local.SelectedCheckout != 1 {
		t.Fatalf("explicit linked context = selected %d checkouts %+v", local.SelectedCheckout, local.Context.Checkouts)
	}
	if len(local.Context.Checkouts[0].Tasks) != 1 || local.Context.Checkouts[0].Tasks[0].ID != tracked.ID {
		t.Fatalf("canonical task omitted from linked-path context: %+v", local.Context)
	}
}

func TestRepoContextJSONSelectsLinkedCWDAndPreservesRuntimeErrorWithoutNetwork(t *testing.T) {
	repository := gittest.New(t)
	worktree := filepath.Join(t.TempDir(), "linked")
	if err := gitx.AddWorktree(context.Background(), repository.Root, worktree, "feat/context", "main"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(worktree)

	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	remotesPath := filepath.Join(root, "remotes.toml")
	if err := os.WriteFile(remotesPath, []byte("schema_version = 1\n[[hosts]]\nname = \"lab\"\nssh_alias = \"must-not-run\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(root, "ssh-was-run")
	if goruntime.GOOS != "windows" {
		bin := filepath.Join(root, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		ssh := filepath.Join(bin, "ssh")
		if err := os.WriteFile(ssh, []byte("#!/bin/sh\nprintf called > "+marker+"\nexit 99\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}

	var stdout, stderr bytes.Buffer
	runtimeErr := context.DeadlineExceeded
	app := &App{
		Cfg: config.Default(), Tasks: task.NewStore(filepath.Join(root, "tasks")),
		Out: &stdout, Err: &stderr, remotesPath: remotesPath,
		runtimeInstance: &activityRuntime{name: "herdr", listErr: runtimeErr},
	}
	cmd := newRepoContextCmd(app)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("repo context --json: %v\nstderr: %s", err, stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("default repo context invoked ssh; marker error = %v", err)
	}

	var report repocontext.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, stdout.String())
	}
	if report.Repository.Path != repository.Root {
		t.Errorf("repository path = %q, want main checkout %q", report.Repository.Path, repository.Root)
	}
	selectedPath := ""
	if report.SelectedCheckout != nil {
		selectedPath = report.SelectedCheckout.Path
	}
	selectedReal, selectedErr := filepath.EvalSymlinks(selectedPath)
	worktreeReal, worktreeErr := filepath.EvalSymlinks(worktree)
	if report.SelectedCheckout == nil || selectedErr != nil || worktreeErr != nil || selectedReal != worktreeReal || report.SelectedCheckout.Canonical {
		t.Fatalf("linked CWD selection = %+v (selected real=%q err=%v, want real=%q err=%v)", report.SelectedCheckout, selectedReal, selectedErr, worktreeReal, worktreeErr)
	}
	if len(report.Local.Checkouts) != 2 || report.Local.LinkedWorktreeCount == nil || *report.Local.LinkedWorktreeCount != 1 {
		t.Fatalf("local checkout inventory = %+v", report.Local)
	}
	if len(report.Local.Runtimes) != 1 || report.Local.Runtimes[0].Error == nil || !strings.Contains(*report.Local.Runtimes[0].Error, runtimeErr.Error()) {
		t.Fatalf("runtime error was not preserved: %+v", report.Local.Runtimes)
	}
	if report.Fleet.Coverage != repocontext.FleetCoverageConfiguredHostsOnly || report.Fleet.ConfiguredHosts == nil || *report.Fleet.ConfiguredHosts != 1 {
		t.Fatalf("fleet coverage = %+v", report.Fleet)
	}
	if len(report.Fleet.Hosts) != 1 || report.Fleet.Hosts[0].SourceID != nil || report.Fleet.Hosts[0].Error == nil {
		t.Fatalf("cache miss must remain unavailable, not live/clean: %+v", report.Fleet.Hosts)
	}
}
