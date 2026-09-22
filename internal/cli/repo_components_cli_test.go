package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/scaffold"
	"github.com/daviddwlee84/dev-cli/internal/testutil"
)

func TestRepoComponentsAcrossNewCloneAndSetupPlans(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("repo", "new", "composed", "--component", "python", "--component", "node", "--dry-run", "--json")
	var result struct {
		Plan scaffold.Plan `json:"plan"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Plan.Components, []string{"python", "node"}) || !slices.Equal(result.Plan.Settings.Gitignore, []string{"python", "node"}) || len(result.Plan.Skills) != 0 {
		t.Fatalf("plan = %+v", result.Plan)
	}
	if _, err := os.Stat(filepath.Join(h.scanRoot, "composed")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created a repository: %v", err)
	}
	out = h.mustRun("repo", "setup", h.repo.Root, "--preset", "minimal", "--component", "go", "--dry-run", "--json")
	if err := json.Unmarshal([]byte(out), &result); err != nil || !slices.Equal(result.Plan.Components, []string{"go"}) {
		t.Fatalf("setup plan: %v\n%s", err, out)
	}
	out = h.mustRun("repo", "clone", h.repo.Root, "--path", filepath.Join(h.scanRoot, "clone-components"), "--preset", "minimal", "--component", "rust", "--yes", "--dry-run", "--json")
	var clone struct {
		Components []string `json:"components"`
	}
	if err := json.Unmarshal([]byte(out), &clone); err != nil || !slices.Equal(clone.Components, []string{"rust"}) {
		t.Fatalf("clone plan: %v\n%s", err, out)
	}
	_, _, err := h.run("repo", "clone", h.repo.Root, "--component", "python", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "require --preset") {
		t.Fatalf("component silently enabled clone setup: %v", err)
	}
}

func TestRepoPythonSkillUsesNativeProviderAndPreservesItsLock(t *testing.T) {
	h := newHarness(t)
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "provider.json")
	testutil.GoCommand(t, bin, "skills", `package main
import ("encoding/json";"os";"path/filepath")
func main() {
 cwd, err := os.Getwd(); if err != nil { panic(err) }
 data, err := json.Marshal(struct{Root string; Args []string}{cwd, os.Args[1:]}); if err != nil { panic(err) }
 if err = os.WriteFile(os.Getenv("SCAFFOLD_PROVIDER_LOG"), data, 0600); err != nil { panic(err) }
 if err = os.WriteFile(filepath.Join(cwd,"skills-lock.json"), []byte("provider-owned-lock\n"), 0644); err != nil { panic(err) }
}
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SCAFFOLD_PROVIDER_LOG", log)
	flags := []string{"--component", "python", "--enable", "python-project-best-practice", "--agent", "codex", "--gitignore", "common", "--yes"}
	args := append([]string{"repo", "new", "python-preview", "--dry-run"}, flags...)
	h.mustRun(args...)
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("dry-run invoked the provider: %v", err)
	}
	args = append([]string{"repo", "new", "python-install"}, flags...)
	h.mustRun(args...)
	destination := filepath.Join(h.scanRoot, "python-install")
	if body, err := os.ReadFile(filepath.Join(destination, "skills-lock.json")); err != nil || string(body) != "provider-owned-lock\n" {
		t.Fatalf("native provider lock changed: %q, %v", body, err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var invocation struct {
		Root string
		Args []string
	}
	if err := json.Unmarshal(data, &invocation); err != nil {
		t.Fatal(err)
	}
	want := []string{"add", "https://github.com/daviddwlee84/agent-skills/tree/main/skills/local/python-project-best-practice", "--skill", "python-project-best-practice", "--agent", "codex", "--yes"}
	if !slices.Equal(invocation.Args, want) || filepath.Base(invocation.Root) != "python-install" {
		t.Fatalf("provider invocation = %+v", invocation)
	}
}

func TestRepoLegacyPlanPlaceholderRemainsExplicitlySelectable(t *testing.T) {
	h := newHarness(t)
	h.mustRun("repo", "new", "explicit-placeholder", "--preset", "agent-ready", "--enable", "claude-plans-directory")
	if _, err := os.Stat(filepath.Join(h.scanRoot, "explicit-placeholder", ".claude", "plans", ".gitkeep")); err != nil {
		t.Fatalf("explicit legacy placeholder is unavailable: %v", err)
	}
}
