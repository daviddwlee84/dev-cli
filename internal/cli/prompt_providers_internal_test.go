package cli

import (
	"errors"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/promptkit"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

func TestPromptModesExposeEveryRegisteredRecipe(t *testing.T) {
	root := newPromptCmd(&App{})
	for _, modeName := range []string{"render", "run", "open"} {
		mode, _, err := root.Find([]string{modeName})
		if err != nil {
			t.Fatal(err)
		}
		available := map[string]bool{}
		for _, command := range mode.Commands() {
			available[command.Name()] = true
		}
		for _, recipe := range promptkit.Recipes() {
			if !available[recipe.Name] {
				t.Errorf("prompt %s does not expose registered recipe %s", modeName, recipe.Name)
			}
		}
	}
}

func TestPRJSONCarriesPartialCollectionWarning(t *testing.T) {
	payload := makePRListJSON(nil, []prProviderStatus{{Forge: forge.GitHub, Status: "ready"}},
		prCollectOptions{Scope: scopeAccount}, errors.New("review query failed"))
	if len(payload.Warnings) != 1 || payload.Warnings[0] != "review query failed" {
		t.Fatalf("warnings = %v", payload.Warnings)
	}
}

func TestMergedPRActionsBindSweepToTheRepository(t *testing.T) {
	actions := prActions(prRow{
		PR:    forge.PullRequest{Forge: forge.GitHub, Repo: "owner/repo", Number: 1, State: forge.PRStateMerged},
		Local: &prLocal{TaskID: "task", RepoPath: "/repo with space"},
	})
	want := "cd '/repo with space' && dev sweep --merged-worktrees"
	if actions["retire"] != want {
		t.Fatalf("retire action = %q, want %q", actions["retire"], want)
	}
}

func TestCloseoutMappingsUseRetirementMixedPanePolicy(t *testing.T) {
	repository := gittest.New(t)
	session := runtime.Session{
		Handle: "w1",
		Panes: []runtime.Pane{
			{ID: "w1:p1", CWD: repository.Root, Agent: "claude", AgentStatus: "idle"},
			// A non-Git pane is still mixed purpose. The former prompt-only
			// mapper ignored it because it could not resolve a worktree root,
			// while retirement correctly blocks the session.
			{ID: "w1:p2", CWD: t.TempDir()},
		},
	}
	mappings := closeoutMappings(t.Context(), session, "")
	if len(mappings) != 1 {
		t.Fatalf("mappings = %+v", mappings)
	}
	mapping := mappings[0]
	if !mapping.CoversTarget || !mapping.MixedPurpose || len(mapping.MixedPaneIDs) != 1 || mapping.MixedPaneIDs[0] != "w1:p2" {
		t.Fatalf("mapping = %+v", mapping)
	}
}
