package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/submodule"
)

func TestSubmoduleAddDryRunAndAlias(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(map[bool]string{false: "submodule", true: "repo"}[alias], func(t *testing.T) {
			app, out := newRepoWizardApp(t, "")
			parent := gittest.New(t)
			cmd := newSubmoduleAddCmd(app, alias)
			cmd.SetArgs([]string{"https://offline.example.test/team/repo.git", "libs/repo", "--parent", parent.Root, "--dry-run", "--json"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var result submodule.AddResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err, out.String())
			}
			if result.Phase != "planned" || result.Checkout != "pinned" || result.Path != "libs/repo" {
				t.Fatal(result)
			}
			if _, err := os.Stat(filepath.Join(parent.Root, "libs")); !os.IsNotExist(err) {
				t.Fatal("dry run wrote destination")
			}
		})
	}
}

func TestSubmoduleAddRequiresApprovalAndSafeSource(t *testing.T) {
	for _, args := range [][]string{
		{"https://host/repo", "--json"},
		{"--json", "--yes"},
		{"https://example:password@host/repo", "--json", "--dry-run"},
		{"https://host/repo", "--checkout=default-branch", "--ref=main", "--json", "--dry-run"},
	} {
		app, _ := newRepoWizardApp(t, "")
		parent := gittest.New(t)
		cmd := newSubmoduleAddCmd(app, false)
		cmd.SetArgs(append(args, "--parent", parent.Root))
		err := cmd.Execute()
		if err == nil || strings.Contains(err.Error(), "password") {
			t.Fatalf("unsafe result: %v", err)
		}
	}
}

func TestSubmoduleSourcePickerCombinesKnownLocalAndCached(t *testing.T) {
	app, _ := newRepoWizardApp(t, "")
	local := gittest.New(t)
	local.Git("remote", "add", "origin", "git@github.com:owner/shared.git")
	app.Cfg.Paths.ScanRoots = []string{filepath.Dir(local.Root)}
	cache := forge.Cache{Version: forge.CacheVersion, SourceID: remoteCacheSourceID(app), FetchedAt: time.Now().UTC(), Complete: true, Repos: []forge.RemoteRepo{
		{Forge: forge.GitHub, Name: "shared", FullName: "owner/shared", CloneURL: "https://github.com/owner/shared.git"},
		{Forge: forge.GitHub, Name: "remote", FullName: "owner/remote", CloneURL: "https://github.com/owner/remote.git"},
	}}
	if err := forge.SaveCacheState(remoteCachePath(), cache); err != nil {
		t.Fatal(err)
	}
	candidates, err := submoduleSourceCandidates(t.Context(), app)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("%+v %v", candidates, err)
	}
	ref, err := resolveSubmoduleSource(t.Context(), app, newPrompter(app), "owner/shared", false)
	if err != nil || ref != "git@github.com:owner/shared.git" {
		t.Fatalf("%q %v", ref, err)
	}
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		if len(request.Items) != 3 {
			t.Fatal(request)
		}
		return picker.Result{}, picker.ErrCanceled
	}
	if _, err := resolveSubmoduleSource(t.Context(), app, newPrompter(app), "", true); !errors.Is(err, picker.ErrCanceled) {
		t.Fatal(err)
	}
}

func TestSubmoduleAddWizardModesAndCancellation(t *testing.T) {
	for _, mode := range []string{"pinned", "default-branch"} {
		t.Run(mode, func(t *testing.T) {
			input := "\n" + mode + "\n"
			if mode == "pinned" {
				input += "\n"
			}
			input += "none\nn\n"
			app, out := newRepoWizardApp(t, input)
			parent := gittest.New(t)
			cmd := newSubmoduleAddCmd(app, false)
			cmd.SetArgs([]string{"https://host/team/repo.git", "--parent", parent.Root})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Canceled") || !strings.Contains(out.String(), mode) {
				t.Fatal(out.String())
			}
			if _, err := os.Stat(filepath.Join(parent.Root, "repo")); !os.IsNotExist(err) {
				t.Fatal("cancel created clone")
			}
		})
	}
}

func TestSubmoduleAddJSONSuccess(t *testing.T) {
	app, out := newRepoWizardApp(t, "")
	parent, source := gittest.New(t), gittest.New(t)
	source.Git("branch", "-m", "trunk")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+filepath.ToSlash(source.Root)+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://source.example.test/repo.git")
	cmd := newSubmoduleAddCmd(app, true)
	cmd.SetArgs([]string{"https://source.example.test/repo.git", "child", "--parent", parent.Root, "--checkout=default-branch", "--yes", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err, out.String())
	}
	var result submodule.AddResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err, out.String())
	}
	if result.Phase != "complete" || result.Branch != "trunk" || !result.Staged {
		t.Fatal(result)
	}
	g, err := gitx.SubmodulesOf(t.Context(), parent.Root)
	if err != nil || len(g.Nodes) != 1 {
		t.Fatalf("%+v %v", g, err)
	}
}

func TestSubmoduleWizardUsesNearestRootPolicy(t *testing.T) {
	app, out := newRepoWizardApp(t, "\n\n\n\n")
	parent := gittest.New(t)
	parent.Write(".dev-cli/config.toml", "[submodules]\ninit = 'none'\n")
	subdir := filepath.Join(parent.Root, "src")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := newSubmoduleAddCmd(app, false)
	cmd.SetArgs([]string{"https://host/team/repo.git", "--parent", subdir, "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "descendants none") {
		t.Fatal(out.String())
	}
}

func TestSubmoduleSourcePickerDeduplicatesAzureTransports(t *testing.T) {
	app, _ := newRepoWizardApp(t, "")
	local := gittest.New(t)
	ssh := "git@ssh.dev.azure.com:v3/acme/Platform/api"
	local.Git("remote", "add", "origin", ssh)
	app.Cfg.Paths.ScanRoots = []string{filepath.Dir(local.Root)}
	remote := forge.RemoteRepo{Forge: forge.AzureDevOps, Name: "api", FullName: "acme/Platform/api", CloneURL: "https://dev.azure.com/acme/Platform/_git/api"}
	cache := forge.Cache{Version: forge.CacheVersion, SourceID: remoteCacheSourceID(app), FetchedAt: time.Now().UTC(), Complete: true, Repos: []forge.RemoteRepo{remote}}
	if err := forge.SaveCacheState(remoteCachePath(), cache); err != nil {
		t.Fatal(err)
	}
	candidates, err := submoduleSourceCandidates(t.Context(), app)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("%+v %v", candidates, err)
	}
	got, err := resolveSubmoduleSource(t.Context(), app, newPrompter(app), remote.Label(), false)
	if err != nil || got != ssh {
		t.Fatalf("%q %v", got, err)
	}
}
