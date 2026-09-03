package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRegisteredWorktreeRejectsCanonicalAliasAmbiguity(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repo")
	commonDir := filepath.Join(repositoryPath, ".git")
	checkoutPath := filepath.Join(root, "checkout")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(checkoutPath, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasPath := filepath.Join(root, "checkout-alias")
	if err := os.Symlink(checkoutPath, aliasPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := resolveRegisteredWorktree([]Worktree{
		{Path: repositoryPath, Main: true, Branch: "main"},
		{Path: checkoutPath, Branch: "feat/one"},
		{Path: aliasPath, Branch: "feat/two"},
	}, commonDir, checkoutPath)
	if !errors.Is(err, ErrWorktreeAmbiguous) {
		t.Fatalf("error = %v, want ErrWorktreeAmbiguous", err)
	}
}
