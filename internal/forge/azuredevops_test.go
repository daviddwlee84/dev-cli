package forge

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestAzureDevOpsListReposKeepsPartialResults(t *testing.T) {
	logPath := installForgeFixture(t, "az", []forgeFixtureRule{
		{Prefix: "extension ", Stdout: "azure-devops\n"},
		{Contains: "--project Platform", Stdout: `[{"name":"zeta","remoteUrl":"https://dev.azure.com/acme/Platform/_git/zeta","project":{"name":"Platform","visibility":"private"}},{"name":"alpha","remoteUrl":"https://dev.azure.com/acme/Platform/_git/alpha","project":{"name":"Platform","visibility":"private"}}]`},
		{Contains: "--project Broken", Stderr: "permission denied\n", Exit: 1},
	})
	adapter := NewAzureDevOps([]AzureDevOpsTarget{
		{Organization: "https://dev.azure.com/acme", Project: "Platform"},
		{Organization: "https://dev.azure.com/acme", Project: "Broken"},
	})
	repos, err := adapter.ListRepos(t.Context())
	if err == nil || !strings.Contains(err.Error(), "Broken") {
		t.Fatalf("partial error = %v", err)
	}
	if len(repos) != 2 || repos[0].FullName != "acme/Platform/alpha" || repos[1].FullName != "acme/Platform/zeta" {
		t.Fatalf("repos = %+v", repos)
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "repos list --detect false --organization https://dev.azure.com/acme --project Platform") {
		t.Fatalf("az invocation:\n%s", log)
	}
}

func TestAzureDevOpsCreatePRReturnsPortalURL(t *testing.T) {
	logPath := installForgeFixture(t, "az", []forgeFixtureRule{
		{Prefix: "extension ", Stdout: "azure-devops\n"},
		{Prefix: "repos pr create ", Stdout: `{"pullRequestId":42,"remoteUrl":"https://acme@dev.azure.com/acme/Platform%20Tools/_git/api"}`},
	})
	adapter := NewAzureDevOps(nil)
	url, err := adapter.CreatePR(t.Context(), t.TempDir(), PRRequest{
		Base: "main", Head: "feat/azure", Title: "Azure support", Body: "Ready", Draft: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://dev.azure.com/acme/Platform%20Tools/_git/api/pullrequest/42" {
		t.Fatalf("URL = %q", url)
	}
	log, _ := os.ReadFile(logPath)
	want := "repos pr create --detect true --source-branch feat/azure --target-branch main --title Azure support --description Ready --draft true"
	if !strings.Contains(string(log), want) {
		t.Fatalf("az invocation:\n%s", log)
	}
}

func TestAzureDevOpsMissingExtensionStopsBeforeReposCommand(t *testing.T) {
	logPath := installForgeFixture(t, "az", []forgeFixtureRule{{Prefix: "extension ", Exit: 1}})
	adapter := NewAzureDevOps(nil)
	_, err := adapter.CreatePR(t.Context(), t.TempDir(), PRRequest{Base: "main", Head: "feat/x"})
	var missing *ErrNoExtension
	if !errors.As(err, &missing) {
		t.Fatalf("error = %T %v", err, err)
	}
	log, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "extension show") {
		t.Fatalf("expected extension preflight only, got:\n%s", log)
	}
}
