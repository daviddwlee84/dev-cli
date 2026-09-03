package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
)

func TestFleetFilesExplicitPathSelectsLinkedCheckoutAndNameSelectsCanonical(t *testing.T) {
	repository := gittest.New(t)
	repository.WithRemote()
	repository.Git("remote", "set-url", "origin", "https://example.test/acme/repo.git")
	repository.Git("branch", "feat/portable")
	linked := filepath.Join(t.TempDir(), "linked")
	repository.Git("worktree", "add", linked, "feat/portable")
	nested := filepath.Join(linked, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Paths.ScanRoots = []string{filepath.Dir(repository.Root)}
	cfg.Paths.RepoPaths = nil
	app := &App{Cfg: cfg}

	checkout, status, _, _, err := resolveFleetFilesSource(t.Context(), app, nested)
	if err != nil {
		t.Fatal(err)
	}
	canonicalLinked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	if checkout != canonicalLinked || status.Branch != "feat/portable" {
		t.Fatalf("explicit linked path selected checkout=%q branch=%q", checkout, status.Branch)
	}

	checkout, status, _, _, err = resolveFleetFilesSource(t.Context(), app, "repo")
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	if checkout != canonicalRoot || status.Branch != "main" {
		t.Fatalf("repository name selected checkout=%q branch=%q", checkout, status.Branch)
	}
}

func TestFleetFilePatternsNeverInheritWorktreeInclude(t *testing.T) {
	worktree := []string{".env"}
	project := projectconfig.Result{Effective: projectconfig.Override{
		Worktree: projectconfig.WorktreeOverride{Include: &worktree},
	}}
	patterns := fleetFilePatterns(project, []string{".adhoc"})
	if len(patterns) != 1 || patterns[0].Value != ".adhoc" || patterns[0].Source != "--file" {
		t.Fatalf("patterns = %+v", patterns)
	}
}
