package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func TestRepoCloneForkRequiresYesOutsideTerminal(t *testing.T) {
	app, out := newRepoWizardApp(t, "")
	app.interactiveCheck = func() bool { return false }
	cmd := newRepoCloneCmd(app)
	cmd.SetArgs([]string{"owner/project", "--fork", "--json"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "requires --yes") {
		t.Fatalf("error=%v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected success output: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(app.Cfg.Paths.ProjectRoot, "project")); !os.IsNotExist(err) {
		t.Fatalf("destination changed: %v", err)
	}
}

func TestRepoForkRequiresYesOutsideTerminal(t *testing.T) {
	app, _ := newRepoWizardApp(t, "")
	app.interactiveCheck = func() bool { return false }
	cmd := newRepoForkCmd(app)
	cmd.SetArgs([]string{"--json"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "requires --yes") {
		t.Fatalf("error=%v", err)
	}
}

func TestCloneDryRunKeepsExistingFieldsAndAddsForkPlanOnlyWhenRequested(t *testing.T) {
	for _, withFork := range []bool{false, true} {
		var out bytes.Buffer
		request := repoWorkflowRequest{
			Kind: repo.AcquireClone, Ref: "owner/project", Destination: "/tmp/project",
			Handoff: repoHandoffStay, CheckIn: repoCheckInNone, JSON: true, DryRun: true,
		}
		if withFork {
			request.ForkPlan = &repo.ForkPlan{Path: request.Destination, SourceURL: "https://github.com/owner/project.git", ForkURL: "https://github.com/me/project.git"}
		}
		if err := renderCloneDryRun(&App{Out: &out}, request); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["operation"] != "clone" || payload["dry_run"] != true || payload["handoff"] != "stay" || payload["check_in"] != "none" {
			t.Fatalf("legacy fields changed: %v", payload)
		}
		_, exists := payload["fork"]
		if exists != withFork {
			t.Fatalf("fork field=%v, requested=%v", exists, withFork)
		}
	}
}

func TestRepoForkConfirmationCancellationIsExplicit(t *testing.T) {
	app, out := newRepoWizardApp(t, "n\n")
	err := confirmRepoFork(app, repo.ForkPlan{Path: "/tmp/project", SourceURL: "source", ForkURL: "fork"}, false)
	if !errors.Is(err, errPromptCanceled) || !strings.Contains(out.String(), "origin") || !strings.Contains(out.String(), "upstream") {
		t.Fatalf("error=%v output=%s", err, out.String())
	}
}

func TestRepoForkPartialResultJSONRetainsCompletedStages(t *testing.T) {
	var out bytes.Buffer
	result := repo.ForkResult{Plan: repo.ForkPlan{Path: "/tmp/project", ForkURL: "https://github.com/me/project.git"}, Completed: []string{"fork", "clone"}}
	if err := renderRepoForkResult(&App{Out: &out}, "clone", result, errors.New("remote rename failed")); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["error"] != "remote rename failed" || payload["path"] != "/tmp/project" {
		t.Fatalf("partial result=%v", payload)
	}
	if !strings.Contains(out.String(), "https://github.com/me/project.git") || !strings.Contains(out.String(), "clone") {
		t.Fatal("partial result lost retained effects")
	}
}
