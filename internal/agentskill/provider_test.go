package agentskill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMutationCommandsUseExplicitProviderArgv(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "skills"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin)
	root := t.TempDir()

	add, err := AddCommand(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, add.Args[1:], []string{"add", DefaultSource})
	install, err := InstallCommand(context.Background(), root, "owner/catalog", []string{"one", "two"}, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, install.Args[1:], []string{"add", "owner/catalog", "--skill", "one", "two", "--agent", "claude-code", "codex", "--yes"})
	project, err := UpdateCommand(context.Background(), root, "one", ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, project.Args[1:], []string{"update", "one", "--yes", "--project"})
	global, err := UpdateCommand(context.Background(), root, "one", ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, global.Args[1:], []string{"update", "one", "--yes", "--global"})
}

func TestMutationCommandsUseNpxOnlyAtExplicitBoundary(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "npx"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin)
	command, err := AddCommand(context.Background(), t.TempDir(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, command.Args[1:], []string{"skills", "add", "owner/repo"})
	status := MutationProviderStatus()
	if !status.Available || status.Command != "npx skills" || !status.DownloadsOnRun {
		t.Fatalf("status = %+v", status)
	}
}

func TestMutationProviderStatusAndProviderVersionNeverRunNpx(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "npx-ran")
	writeExecutable(t, filepath.Join(bin, "npx"), "#!/bin/sh\nprintf ran > \""+marker+"\"\n")
	t.Setenv("PATH", bin)
	status := MutationProviderStatus()
	if !status.Available || status.Path == "" {
		t.Fatalf("status = %+v", status)
	}
	if _, err := ProviderVersion(context.Background(), t.TempDir()); err == nil {
		t.Fatal("ProviderVersion unexpectedly accepted npx")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("npx was executed by a status/version read: %v", err)
	}
}

func TestProviderVersionRunsOnlyDirectSkillsBinary(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "skills"), "#!/bin/sh\nprintf '1.5.23\\n'\n")
	t.Setenv("PATH", bin)
	version, err := ProviderVersion(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if version != "skills 1.5.23" {
		t.Fatalf("version = %q", version)
	}
}

func assertArgv(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}
