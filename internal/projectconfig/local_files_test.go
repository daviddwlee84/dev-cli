package projectconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
)

func TestLocalFilesIncludePreservesEmptyAndProvenance(t *testing.T) {
	root := t.TempDir()
	path := writeProjectFile(t, root, projectconfig.ConfigFilename, `[worktree]
include = [".legacy-env"]
[local_files]
include = []
`)
	result, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Effective.LocalFiles.Include == nil || len(*result.Effective.LocalFiles.Include) != 0 {
		t.Fatalf("explicit empty local_files.include was lost: %+v", result.Effective.LocalFiles.Include)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := result.SourceFor("local_files.include"); !ok || got != canonicalPath {
		t.Fatalf("local_files.include source = %q, %v", got, ok)
	}
	if result.Effective.Worktree.Include == nil || len(*result.Effective.Worktree.Include) != 1 {
		t.Fatal("worktree.include and local_files.include were incorrectly coupled")
	}
}

func TestLocalFilesIncludeOverlaysLegacyPerField(t *testing.T) {
	root := t.TempDir()
	legacyValues := []string{".legacy"}
	legacy := &projectconfig.Layer{Source: "legacy", Override: projectconfig.Override{
		LocalFiles: projectconfig.LocalFilesOverride{Include: &legacyValues},
	}}
	projectValues := `[local_files]
include = [".env", ".mcp/**"]
`
	writeProjectFile(t, root, projectconfig.ConfigFilename, projectValues)
	result, err := projectconfig.Load(root, legacy)
	if err != nil {
		t.Fatal(err)
	}
	got := *result.Effective.LocalFiles.Include
	if len(got) != 2 || got[0] != ".env" || got[1] != ".mcp/**" {
		t.Fatalf("effective local files = %v", got)
	}
}

func TestLocalFilesRejectsNonPortableGlobGrammar(t *testing.T) {
	for _, pattern := range []string{"../.env", `.mcp\\**`, "/absolute", "x[0-9]", "foo/**bar", ".git/config", "node_modules/**"} {
		t.Run(pattern, func(t *testing.T) {
			root := t.TempDir()
			contents := "[local_files]\ninclude = [" + tomlQuote(pattern) + "]\n"
			if err := os.MkdirAll(filepath.Join(root, projectconfig.DirectoryName), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, projectconfig.ConfigRelativePath), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := projectconfig.Load(root, nil); err == nil {
				t.Fatalf("pattern %q should fail", pattern)
			}
		})
	}
}

func tomlQuote(value string) string {
	quoted := "'"
	for _, r := range value {
		if r == '\'' {
			quoted += "''"
		} else {
			quoted += string(r)
		}
	}
	return quoted + "'"
}
