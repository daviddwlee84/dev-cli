package forge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAzureDevOpsListReposKeepsPartialResults(t *testing.T) {
	logPath := installFakeAz(t, `
if [ "$1" = "extension" ]; then
  printf 'azure-devops\n'
  exit 0
fi
case "$*" in
  *"--project Platform"*)
    printf '%s\n' '[{"name":"zeta","remoteUrl":"https://dev.azure.com/acme/Platform/_git/zeta","project":{"name":"Platform","visibility":"private"}},{"name":"alpha","remoteUrl":"https://dev.azure.com/acme/Platform/_git/alpha","project":{"name":"Platform","visibility":"private"}}]'
    ;;
  *"--project Broken"*)
    printf 'permission denied\n' >&2
    exit 1
    ;;
  *) exit 2 ;;
esac
`)
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
	logPath := installFakeAz(t, `
if [ "$1" = "extension" ]; then
  printf 'azure-devops\n'
  exit 0
fi
if [ "$1" = "repos" ] && [ "$2" = "pr" ] && [ "$3" = "create" ]; then
  printf '%s\n' '{"pullRequestId":42,"remoteUrl":"https://acme@dev.azure.com/acme/Platform%20Tools/_git/api"}'
  exit 0
fi
exit 2
`)
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
	logPath := installFakeAz(t, `
if [ "$1" = "extension" ]; then
  exit 1
fi
printf 'unexpected command\n' >&2
exit 2
`)
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

func installFakeAz(t *testing.T, body string) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "az.log")
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >> \"$AZ_TEST_LOG\"\n" + body
	if err := os.WriteFile(filepath.Join(binDir, "az"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZ_TEST_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}
