package agentskill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/testutil"
)

func TestCheckUpdatesHashesGitObjectsWithoutAutocrlfCheckout(t *testing.T) {
	remote := initSkillRepo(t, "one\n")
	expected, err := gitFolderHashContext(context.Background(), remote, "skills/demo")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.autocrlf")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	lock := &LockMetadata{
		Scope: ScopeProject, Source: remote, SourceType: "git",
		SkillPath: "skills/demo/SKILL.md", ComputedHash: expected,
	}
	rows := CheckUpdates(context.Background(), []Skill{{
		Name: "demo", ManagedBy: ManagedBySkills, UpdateStatus: UpdateUnchecked, Lock: lock,
	}})
	if rows[0].UpdateStatus != UpdateCurrent {
		t.Fatalf("autocrlf object status = %+v", rows[0])
	}
}

func TestCheckUpdatesDoesNotRunConfiguredSmudgeFilter(t *testing.T) {
	remote := initSkillRepo(t, "one\n")
	work := filepath.Join(t.TempDir(), "work")
	mustRun(t, "", "git", "clone", "-q", remote, work)
	mustWrite(t, filepath.Join(work, ".gitattributes"), "skills/demo/SKILL.md filter=dev-marker\n")
	mustRun(t, work, "git", "add", ".gitattributes")
	mustRun(t, work, "git", "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-qm", "attributes")
	mustRun(t, work, "git", "push", "-q", "origin", "HEAD")
	expected, err := gitFolderHashContext(context.Background(), remote, "skills/demo")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "smudge-ran")
	script := testutil.GoCommand(t, t.TempDir(), "smudge", `package main
import ("io";"os")
func main() {if err:=os.WriteFile(os.Getenv("DEV_SKILL_SMUDGE_MARKER"),[]byte("ran"),0600);err!=nil {panic(err)};if _,err:=io.Copy(os.Stdout,os.Stdin);err!=nil {panic(err)}}
`)
	t.Setenv("DEV_SKILL_SMUDGE_MARKER", marker)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "filter.dev-marker.smudge")
	t.Setenv("GIT_CONFIG_VALUE_0", `"`+filepath.ToSlash(script)+`"`)
	lock := &LockMetadata{
		Scope: ScopeProject, Source: remote, SourceType: "git",
		SkillPath: "skills/demo/SKILL.md", ComputedHash: expected,
	}
	rows := CheckUpdates(context.Background(), []Skill{{
		Name: "demo", ManagedBy: ManagedBySkills, UpdateStatus: UpdateUnchecked, Lock: lock,
	}})
	if rows[0].UpdateStatus != UpdateCurrent {
		t.Fatalf("smudge object status = %+v", rows[0])
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("smudge filter executed: %v", err)
	}
}

func TestCheckUpdatesMarksNonASCIIFolderHashUnverifiable(t *testing.T) {
	remote := initSkillRepo(t, "one\n")
	work := filepath.Join(t.TempDir(), "work")
	mustRun(t, "", "git", "clone", "-q", remote, work)
	mustWrite(t, filepath.Join(work, "skills", "demo", "κ.md"), "unicode\n")
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-qm", "unicode")
	mustRun(t, work, "git", "push", "-q", "origin", "HEAD")
	lock := &LockMetadata{
		Scope: ScopeProject, Source: remote, SourceType: "git",
		SkillPath: "skills/demo/SKILL.md", ComputedHash: strings.Repeat("a", 64),
	}
	rows := CheckUpdates(context.Background(), []Skill{{
		Name: "demo", ManagedBy: ManagedBySkills, UpdateStatus: UpdateUnchecked, Lock: lock,
	}})
	if rows[0].UpdateStatus != UpdateUnknown || !strings.Contains(rows[0].UpdateDetail, "non-ASCII") {
		t.Fatalf("non-ASCII status = %+v", rows[0])
	}
}
