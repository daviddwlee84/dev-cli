package forge

import (
	"encoding/json"
	"fmt"
	"os"
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
	t.Setenv("GITLAB_HOST", "gitlab.example.com")
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
	if !strings.Contains(string(invocations), "--hostname gitlab.example.com") ||
		!strings.Contains(string(invocations), "membership=true") || !strings.Contains(string(invocations), "page=2") {
		t.Fatalf("membership pagination invocations:\n%s", invocations)
	}
}

func installPagedCLI(t *testing.T, name string, page1, page2 any) string {
	t.Helper()
	first, err := json.Marshal(page1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(page2)
	if err != nil {
		t.Fatal(err)
	}
	return installForgeFixture(t, name, []forgeFixtureRule{
		{Arg: "page=1", Stdout: string(first)},
		{Arg: "page=2", Stdout: string(second)},
		{Stdout: "[]"},
	})
}
