package agentskill

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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
	assertArgv(t, add.Command.Args[1:], []string{"add", DefaultSource})
	install, err := InstallCommand(context.Background(), root, "owner/catalog", []string{"one", "two"}, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, install.Command.Args[1:], []string{"add", "owner/catalog", "--skill", "one", "two", "--agent", "claude-code", "codex", "--yes"})
	project, err := UpdateCommand(context.Background(), root, "one", ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, project.Command.Args[1:], []string{"update", "--yes", "--project", "one"})
	global, err := UpdateCommand(context.Background(), root, "one", ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, global.Command.Args[1:], []string{"update", "--yes", "--global", "one"})
}

func TestUpdateCommandRejectsReservedAndOptionLikeSkillNames(t *testing.T) {
	for _, name := range []string{"--global", "dev-cli", "dev_cli"} {
		if _, err := UpdateCommand(context.Background(), t.TempDir(), name, ScopeProject); err == nil {
			t.Errorf("unsafe skill name %q reached the provider", name)
		}
	}
}

func TestMutationProviderRejectsRepositoryAndNodeModulesExecutables(t *testing.T) {
	repository := t.TempDir()
	for _, relative := range []string{filepath.Join("node_modules", ".bin", "skills"), filepath.Join("tools", "skills")} {
		path := filepath.Join(repository, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeExecutable(t, path, "#!/bin/sh\nexit 0\n")
		t.Setenv("PATH", filepath.Dir(path))
		if status := MutationProviderStatusFor(repository); status.Available {
			t.Fatalf("repository provider accepted: %+v", status)
		}
		if _, err := AddCommand(context.Background(), repository, "owner/repo"); err == nil {
			t.Fatalf("repository provider %s reached mutation", path)
		}
	}
	outside := filepath.Join(t.TempDir(), "trusted-looking-target")
	writeExecutable(t, outside, "#!/bin/sh\nexit 0\n")
	link := filepath.Join(repository, "bin", "skills")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(link))
	if status := MutationProviderStatusFor(repository); status.Available {
		t.Fatalf("repository-controlled provider symlink accepted: %+v", status)
	}
}

func TestMutationCommandsNeverInvokeNpxFromRepositoryCheckout(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "npx-ran")
	writeExecutable(t, filepath.Join(bin, "npx"), "#!/bin/sh\nprintf ran > \""+marker+"\"\n")
	t.Setenv("PATH", bin)
	if _, err := AddCommand(context.Background(), t.TempDir(), "owner/repo"); err == nil {
		t.Fatal("AddCommand unexpectedly accepted npx as a repository-scoped provider")
	}
	status := MutationProviderStatus()
	if status.Available || status.Path == "" || !strings.Contains(status.Detail, "not invoked") {
		t.Fatalf("status = %+v", status)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("npx was executed: %v", err)
	}
}

func TestMutationProviderStatusAndProviderVersionNeverRunNpx(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "npx-ran")
	writeExecutable(t, filepath.Join(bin, "npx"), "#!/bin/sh\nprintf ran > \""+marker+"\"\n")
	t.Setenv("PATH", bin)
	status := MutationProviderStatus()
	if status.Available || status.Path == "" {
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

func TestMutationProviderSkipsUntrustedPathCandidate(t *testing.T) {
	repository := t.TempDir()
	localBin := filepath.Join(repository, "node_modules", ".bin")
	trustedBin := t.TempDir()
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(localBin, "skills"), "#!/bin/sh\nexit 99\n")
	writeExecutable(t, filepath.Join(trustedBin, "skills"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", localBin+string(os.PathListSeparator)+trustedBin)

	status := MutationProviderStatusFor(repository)
	trustedPath, err := filepath.EvalSymlinks(filepath.Join(trustedBin, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if !status.Available || status.Path != trustedPath {
		t.Fatalf("provider status = %+v", status)
	}
}

func TestInstallCommandValidatesEffectiveRepositoryRoot(t *testing.T) {
	repository := initRepository(t)
	nested := filepath.Join(repository, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(repository, "tools")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "skills"), "#!/bin/sh\nexit 0\n")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if _, err := InstallCommand(context.Background(), nested, "owner/repo", []string{"demo"}, []string{"claude-code"}); err == nil {
		t.Fatal("provider inside effective repository root was accepted")
	}
}

func TestLockUpdateableRequiresRecognizedSource(t *testing.T) {
	base := LockMetadata{Name: "demo", SkillPath: "skills/demo/SKILL.md"}
	for _, test := range []struct {
		name  string
		entry LockMetadata
		want  bool
	}{
		{name: "missing source", entry: base},
		{name: "traversal path", entry: func() LockMetadata {
			entry := base
			entry.SourceType, entry.Source, entry.SkillPath = "github", "owner/repo", "../../outside/SKILL.md"
			return entry
		}()},
		{name: "unknown source", entry: func() LockMetadata {
			entry := base
			entry.SourceType, entry.Source = "mystery", "owner/repo"
			return entry
		}()},
		{name: "github", entry: func() LockMetadata {
			entry := base
			entry.SourceType, entry.Source = "github", "owner/repo"
			return entry
		}(), want: true},
		{name: "git", entry: func() LockMetadata {
			entry := base
			entry.SourceType, entry.SourceURL = "git", "https://example.test/repo.git"
			return entry
		}(), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := lockUpdateable(test.entry); got != test.want {
				t.Fatalf("lockUpdateable(%+v) = %v, want %v", test.entry, got, test.want)
			}
		})
	}
}

func TestNodeModulesPathCheckIsCaseInsensitive(t *testing.T) {
	if !pathUsesNodeModulesBin(`C:\repo\Node_Modules\.BIN\skills.cmd`) {
		t.Fatal("mixed-case Windows node_modules shim was accepted")
	}
}

func assertArgv(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}
