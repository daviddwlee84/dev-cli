package forge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubListReposPaginatesAllVisibleRepos(t *testing.T) {
	page1 := make([]map[string]any, 100)
	for i := range page1 {
		page1[i] = map[string]any{"name": fmt.Sprintf("r%d", i), "full_name": fmt.Sprintf("owner/r%d", i), "visibility": "private"}
	}
	page2 := []map[string]any{{"name": "last", "full_name": "owner/last", "visibility": "public"}}
	log := installPagedCLI(t, "gh", page1, page2)
	repos, err := (&gh{}).ListRepos(t.Context())
	if err != nil || len(repos) != 101 {
		t.Fatalf("repos = %d, %v", len(repos), err)
	}
	invocations, _ := os.ReadFile(log)
	if !strings.Contains(string(invocations), "page=1") || !strings.Contains(string(invocations), "page=2") {
		t.Fatalf("pagination invocations:\n%s", invocations)
	}
}

func TestGitLabListReposUsesMembershipAndPaginates(t *testing.T) {
	page1 := make([]map[string]any, 100)
	for i := range page1 {
		page1[i] = map[string]any{"name": fmt.Sprintf("r%d", i), "path_with_namespace": fmt.Sprintf("group/r%d", i), "visibility": "private"}
	}
	page2 := []map[string]any{{"name": "last", "path_with_namespace": "group/last", "visibility": "internal"}}
	log := installPagedCLI(t, "glab", page1, page2)
	repos, err := (&glab{}).ListRepos(t.Context())
	if err != nil || len(repos) != 101 {
		t.Fatalf("repos = %d, %v", len(repos), err)
	}
	invocations, _ := os.ReadFile(log)
	if !strings.Contains(string(invocations), "membership=true") || !strings.Contains(string(invocations), "page=2") {
		t.Fatalf("membership pagination invocations:\n%s", invocations)
	}
}

func installPagedCLI(t *testing.T, name string, page1, page2 any) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	writeJSON := func(name string, value any) string {
		path := filepath.Join(dir, name)
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	p1, p2 := writeJSON("page1.json", page1), writeJSON("page2.json", page2)
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$PAGED_CLI_LOG"
page=
for arg in "$@"; do
  case "$arg" in page=*) page="$arg" ;; esac
done
case "$page" in
  page=1) exec cat "$PAGED_CLI_PAGE1" ;;
  page=2) exec cat "$PAGED_CLI_PAGE2" ;;
  *) printf '[]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAGED_CLI_LOG", log)
	t.Setenv("PAGED_CLI_PAGE1", p1)
	t.Setenv("PAGED_CLI_PAGE2", p2)
	return log
}
