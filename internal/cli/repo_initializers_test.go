package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

func TestApplyRepoInitializersIsIdempotent(t *testing.T) {
	root := t.TempDir()
	selection := repoInitSelection{
		Name: "demo", Description: "An example", README: true,
		Gitignore: []string{"common"}, License: "mit", LicenseHolder: "Demo Owner",
		ClaudePlans: true, AgentContract: true,
	}
	first, err := applyRepoInitializers(t.Context(), root, selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Touched) < 5 {
		t.Fatalf("first result = %+v", first)
	}
	second, err := applyRepoInitializers(t.Context(), root, selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Touched) != 0 {
		t.Fatalf("second run changed files: %+v", second)
	}
	settings, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if !strings.Contains(string(settings), "plansDirectory") {
		t.Fatalf("settings = %s", settings)
	}
}

func TestApplyRepoInitializersPreservesExistingReadmeAndAgents(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"README.md", "AGENTS.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := applyRepoInitializers(t.Context(), root, repoInitSelection{
		Name: "demo", README: true, AgentContract: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Touched) != 0 || len(result.Skipped) != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestApplyRepoGitignoreRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), ".gitignore")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".gitignore")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, _, err := applyRepoGitignore(t.Context(), root, []string{"common"})
	if !errors.Is(err, pathx.ErrOutsideRoot) {
		t.Fatalf("symlink escape error = %v", err)
	}
	body, readErr := os.ReadFile(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != "keep\n" {
		t.Fatalf("outside file was changed: %q", body)
	}
}

func TestRepoDestinationRejectsUnsafeCategory(t *testing.T) {
	root := t.TempDir()
	for _, category := range []string{"../outside", filepath.Join(root, "absolute")} {
		if _, err := repoDestination(root, category, "demo", ""); err == nil {
			t.Fatalf("unsafe category %q was accepted", category)
		}
	}
}
