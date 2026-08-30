package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

type publishTestForge struct {
	remote       string
	publishCalls *int
}

func TestRepoDryRunPublishJSONUsesStableFieldNames(t *testing.T) {
	var out bytes.Buffer
	app := &App{Out: &out}
	err := renderRepoDryRun(app, repoWorkflowRequest{
		Kind: repo.AcquireNew, Destination: "/tmp/demo", JSON: true,
		Publish: &repoPublishRequest{
			Forge: forge.GitHub, Name: "demo", Visibility: forge.VisibilityPrivate, Push: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	publish, ok := payload["publish"].(map[string]any)
	if !ok || publish["forge"] != "github" || publish["name"] != "demo" {
		t.Fatalf("publish JSON = %#v", payload["publish"])
	}
	if _, legacy := publish["Forge"]; legacy {
		t.Fatalf("publish JSON leaked Go field names: %#v", publish)
	}
}

func (f publishTestForge) Kind() forge.Kind           { return forge.GitHub }
func (f publishTestForge) Bin() string                { return "gh" }
func (f publishTestForge) Available() bool            { return true }
func (f publishTestForge) CloneURL(ref string) string { return ref }
func (f publishTestForge) ListRepos(context.Context) ([]forge.RemoteRepo, error) {
	return nil, nil
}
func (f publishTestForge) CreatePR(context.Context, string, forge.PRRequest) (string, error) {
	return "", nil
}
func (f publishTestForge) CreateRepo(context.Context, string, forge.RepoRequest) (string, error) {
	return f.remote, nil
}
func (f publishTestForge) PublishRepo(context.Context, string, forge.RepoRequest) (forge.CreateRepoResult, error) {
	if f.publishCalls != nil {
		*f.publishCalls++
	}
	return forge.CreateRepoResult{Forge: forge.GitHub, URL: f.remote, CloneURL: f.remote, RemoteURL: f.remote, RemoteName: "origin"}, nil
}

func TestPublishRepositoryAddsOriginAndPushes(t *testing.T) {
	// The public helper selects the real adapter, so this focused test locks in
	// the shared Git transaction through a temporary gh shim and bare remote.
	bin := t.TempDir()
	gh := filepath.Join(bin, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	root := filepath.Join(t.TempDir(), "repo")
	if _, err := gitx.Run(t.Context(), "", "init", "-b", "main", root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = gitx.Run(t.Context(), root, "add", "README.md")
	if _, err := gitx.Run(t.Context(), root, "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	if _, err := gitx.Run(t.Context(), "", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}

	// Test the transaction body using the same normalized result a publisher
	// returns; adapter command construction is covered in internal/forge.
	created := publishTestForge{remote: remote}
	result, err := publishRepositoryWithForge(t.Context(), root, created, repoPublishRequest{
		Forge: forge.GitHub, Name: "demo", Visibility: forge.VisibilityPrivate, Push: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Added || !result.Pushed || result.Branch != "main" {
		t.Fatalf("result = %+v", result)
	}
}

func TestPublishRepositoryRefusesExistingOriginBeforeForgeMutation(t *testing.T) {
	root := t.TempDir()
	if _, err := gitx.Run(t.Context(), root, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(t.Context(), root, "remote", "add", "origin", "https://example.test/existing.git"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := publishRepositoryWithForge(t.Context(), root, publishTestForge{
		remote: "https://example.test/new.git", publishCalls: &calls,
	}, repoPublishRequest{Forge: forge.GitHub, Name: "demo"})
	if err == nil || calls != 0 {
		t.Fatalf("error = %v, publish calls = %d", err, calls)
	}
}
