package agentskill

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestListMergesScopesAndUsesGitRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "", "git", "init", "-q", repo)
	repo, _ = filepath.EvalSymlinks(repo)
	nested := filepath.Join(repo, "a", "b")
	mustMkdir(t, nested)

	projectPath := filepath.Join(repo, ".agents", "skills", "shared")
	globalPath := filepath.Join(home, ".agents", "skills", "shared")
	writeLock(t, filepath.Join(repo, "skills-lock.json"), map[string]any{
		"shared": map[string]any{"source": "owner/repo", "sourceType": "github", "skillPath": "skills/shared/SKILL.md", "computedHash": strings.Repeat("a", 64)},
	})
	writeLock(t, filepath.Join(home, ".agents", ".skill-lock.json"), map[string]any{
		"shared": map[string]any{"source": "owner/repo/skills", "sourceType": "github", "sourceUrl": "https://github.com/owner/repo.git", "skillPath": "skills/shared/SKILL.md", "skillFolderHash": strings.Repeat("b", 40)},
	})

	bin := t.TempDir()
	script := `#!/bin/sh
case " $* " in
  *" --global "*) scope=global; path="$HOME/.agents/skills/shared" ;;
  *) scope=project; path="$PWD/.agents/skills/shared" ;;
esac
printf '[{"name":"shared","path":"%s","scope":"%s","agents":["Claude Code","Codex"],"source":"owner/repo","sourceUrl":null,"sourceType":"github"}]\n' "$path" "$scope"
`
	writeExecutable(t, filepath.Join(bin, "skills"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	rows, err := List(context.Background(), nested, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	if rows[0].Scope != ScopeProject || rows[0].ScopeRoot != repo || rows[0].Path != projectPath {
		t.Errorf("project row = %+v", rows[0])
	}
	if rows[1].Scope != ScopeGlobal || rows[1].Path != globalPath {
		t.Errorf("global row = %+v", rows[1])
	}
	for _, row := range rows {
		if row.ManagedBy != ManagedBySkills || row.UpdateStatus != UpdateUnchecked {
			t.Errorf("managed row = %+v", row)
		}
	}
}

func TestReadOnlyListDoesNotFallbackToDownloadingNpx(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "called")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"" + marker + "\"\nexit 1\n"
	writeExecutable(t, filepath.Join(bin, "npx"), script)
	t.Setenv("PATH", bin)
	_, err := List(context.Background(), home, ListOptions{Project: true})
	if err == nil {
		t.Fatal("missing cached provider should fail")
	}
	b, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.TrimSpace(string(b)); got != "--no-install skills list --json" {
		t.Fatalf("npx args = %q", got)
	}
}

func TestCheckUpdatesUsesRecordedHashWithoutMutatingInstall(t *testing.T) {
	remote := initSkillRepo(t, "one\n")
	clone := filepath.Join(t.TempDir(), "installed")
	mustRun(t, "", "git", "clone", "-q", remote, clone)
	skillDir := filepath.Join(clone, "skills", "demo")
	hash, err := folderHash(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(t.TempDir(), "demo")
	mustMkdir(t, installed)
	if err := os.WriteFile(filepath.Join(installed, "SKILL.md"), []byte("installed stays\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	row := Skill{
		Name: "demo", Scope: ScopeProject, Path: installed,
		ManagedBy: ManagedBySkills, UpdateStatus: UpdateUnchecked,
		lock: &lockEntry{Source: remote, SourceType: "git", SkillPath: "skills/demo/SKILL.md", ComputedHash: hash},
	}

	got := CheckUpdates(context.Background(), []Skill{row})
	if got[0].UpdateStatus != UpdateCurrent {
		t.Fatalf("current status = %+v", got[0])
	}
	if b, _ := os.ReadFile(filepath.Join(installed, "SKILL.md")); string(b) != "installed stays\n" {
		t.Fatalf("installed skill was mutated: %q", b)
	}

	work := filepath.Join(t.TempDir(), "work")
	mustRun(t, "", "git", "clone", "-q", remote, work)
	if err := os.WriteFile(filepath.Join(work, "skills", "demo", "SKILL.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-qm", "update")
	mustRun(t, work, "git", "push", "-q", "origin", "HEAD")

	got = CheckUpdates(context.Background(), []Skill{row})
	if got[0].UpdateStatus != UpdateAvailable {
		t.Fatalf("changed status = %+v", got[0])
	}
}

func TestCheckUpdatesReportsMissingAndUnverifiable(t *testing.T) {
	remote := initSkillRepo(t, "one\n")
	rows := []Skill{
		{Name: "gone", Scope: ScopeProject, ManagedBy: ManagedBySkills, lock: &lockEntry{Source: remote, SourceType: "git", SkillPath: "skills/gone/SKILL.md", ComputedHash: strings.Repeat("a", 64)}},
		{Name: "local", Scope: ScopeProject, ManagedBy: ManagedBySkills, lock: &lockEntry{Source: "./skill", SourceType: "local", SkillPath: "SKILL.md", ComputedHash: strings.Repeat("a", 64)}},
	}
	got := CheckUpdates(context.Background(), rows)
	if got[0].UpdateStatus != UpdateMissing || got[1].UpdateStatus != UpdateUnknown {
		t.Fatalf("statuses = %+v", got)
	}
}

func TestCommandsUseDefaultSourceAndExactScope(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "skills"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	add, err := AddCommand(context.Background(), t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(add.Args[1:], " ") != "add "+DefaultSource {
		t.Fatalf("add args = %v", add.Args)
	}
	update, err := UpdateCommand(context.Background(), t.TempDir(), "demo", ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(update.Args[1:], " ") != "update demo --yes --global" {
		t.Fatalf("update args = %v", update.Args)
	}
	install, err := InstallCommand(context.Background(), t.TempDir(), "owner/catalog", []string{"one", "two"}, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(install.Args[1:], " ") != "add owner/catalog --skill one two --agent claude-code codex --yes" {
		t.Fatalf("install args = %v", install.Args)
	}
}

func initSkillRepo(t *testing.T, body string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	remote := filepath.Join(t.TempDir(), "remote.git")
	mustRun(t, "", "git", "init", "-q", work)
	mustMkdir(t, filepath.Join(work, "skills", "demo"))
	if err := os.WriteFile(filepath.Join(work, "skills", "demo", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-qm", "initial")
	mustRun(t, "", "git", "init", "-q", "--bare", remote)
	mustRun(t, work, "git", "remote", "add", "origin", remote)
	mustRun(t, work, "git", "push", "-q", "-u", "origin", "HEAD")
	head := strings.TrimSpace(mustRun(t, work, "git", "branch", "--show-current"))
	mustRun(t, remote, "git", "symbolic-ref", "HEAD", "refs/heads/"+head)
	return remote
}

func writeLock(t *testing.T, filename string, skills map[string]any) {
	t.Helper()
	mustMkdir(t, filepath.Dir(filename))
	b, err := json.Marshal(map[string]any{"version": 1, "skills": skills})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, filename, body string) {
	t.Helper()
	if err := os.WriteFile(filename, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, dir, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", bin, args, err, out)
	}
	return string(out)
}
