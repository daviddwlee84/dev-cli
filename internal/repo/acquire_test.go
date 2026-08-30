package repo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func TestAcquireNewInitializesMainWithoutScaffolding(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "projects", "demo")
	result, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "demo", Destination: destination,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := pathx.Canonical(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || !result.GitInited || result.Cloned || result.Path != canonical {
		t.Fatalf("result = %+v", result)
	}
	status, err := gitx.StatusOf(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "main" {
		t.Fatalf("branch = %q", status.Branch)
	}
	if _, err := os.Stat(filepath.Join(destination, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("acquisition unexpectedly scaffolded README: %v", err)
	}
}

func TestAcquireCloneUsesExactDestination(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "source", Destination: source,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(t.Context(), source, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(t.Context(), source, "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "clones", "copy")
	result, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireClone, CloneRef: source, Destination: destination,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := pathx.Canonical(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Cloned || result.Name != "source" || result.Path != canonical {
		t.Fatalf("result = %+v", result)
	}
	if body, err := os.ReadFile(filepath.Join(destination, "README.md")); err != nil || string(body) != "source\n" {
		t.Fatalf("cloned README = %q, %v", body, err)
	}
}

func TestAcquireRejectsExistingAndNestedDestinations(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "repo", Destination: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "again", Destination: root,
	}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing destination error = %v", err)
	}
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "nested", Destination: filepath.Join(root, "nested"),
	}); err == nil || !strings.Contains(err.Error(), "nested repository") {
		t.Fatalf("nested destination error = %v", err)
	}
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "deep", Destination: filepath.Join(root, "missing", "deep"),
	}); err == nil || !strings.Contains(err.Error(), "nested repository") {
		t.Fatalf("deep nested destination error = %v", err)
	}
}

func TestNameFromRef(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"owner/repo", "repo"},
		{"https://github.com/owner/repo.git", "repo"},
		{"git@github.com:owner/repo.git", "repo"},
	} {
		if got := repo.NameFromRef(test.input); got != test.want {
			t.Errorf("NameFromRef(%q) = %q", test.input, got)
		}
	}
}

func TestNormalizeCloneRefPrefersExistingLocalPathsOverForgeLookingSegments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "github.com", "acme", "local.git")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := repo.NormalizeCloneRef(root); got != filepath.Clean(root) {
		t.Fatalf("absolute local path normalized to %q", got)
	}
	parent := filepath.Dir(filepath.Dir(filepath.Dir(root)))
	t.Chdir(parent)
	relative := filepath.Join("github.com", "acme", "local.git")
	want, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	if got := repo.NormalizeCloneRef(relative); got != want {
		t.Fatalf("relative local path normalized to %q, want %q", got, want)
	}
}
