package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/agenthistory"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestArtifactHistoryCLISetupArchiveAndFind(t *testing.T) {
	h := newHarness(t)
	a := gittest.New(t)
	const sid = "01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"
	out := h.mustRun("artifact", "setup", "--repo", h.repo.Root, "--mode", "archive", "--source", "specstory", "--archive", a.Root, "--protection", "off", "--json")
	var p agenthistory.Plan
	if e := json.Unmarshal([]byte(out), &p); e != nil {
		t.Fatal(e)
	}
	h.mustRun("artifact", "setup", "--repo", h.repo.Root, "--apply", "--plan", p.ID, "--yes", "--json")
	h.repo.Write(".specstory/history/chat.md", "# chat\n<!-- Codex CLI Session "+sid+" (2026-09-12) -->\nfind-this-evidence\n")
	out = h.mustRun("artifact", "archive", "--repo", h.repo.Root, "--session", "codex:"+sid, "--json")
	if e := json.Unmarshal([]byte(out), &p); e != nil {
		t.Fatal(e)
	}
	if _, _, e := h.run("artifact", "archive", "--repo", h.repo.Root, "--apply", "--plan", p.ID, "--yes"); e == nil {
		t.Fatal("CLI accepted missing recorder attestation")
	}
	h.mustRun("artifact", "archive", "--repo", h.repo.Root, "--apply", "--plan", p.ID, "--yes", "--writer-stopped", "--json")
	out = h.mustRun("artifact", "find", "--repo", h.repo.Root, "--session", "codex:"+sid, "--json")
	var matches []agenthistory.Match
	if e := json.Unmarshal([]byte(out), &matches); e != nil || len(matches) != 1 {
		t.Fatalf("find output: %v", e)
	}
	if strings.Contains(out, "find-this-evidence") {
		t.Fatal("transcript body escaped structured search output")
	}
}

func TestRepoArtifactSetupUsesSamePlanWithoutScaffold(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("repo", "setup", h.repo.Root, "--artifacts", "--mode", "unmanaged", "--json")
	var p agenthistory.Plan
	if e := json.Unmarshal([]byte(out), &p); e != nil || p.Kind != "artifact_setup" {
		t.Fatalf("repository delegation: %v", e)
	}
	h.mustRun("repo", "setup", h.repo.Root, "--artifacts", "--apply", "--plan", p.ID, "--yes", "--json")
	out = h.mustRun("artifact", "status", "--repo", h.repo.Root, "--json")
	if !strings.Contains(out, `"mode": "unmanaged"`) {
		t.Fatal("policy not visible through artifact adapter")
	}
	if _, _, e := h.run("repo", "setup", h.repo.Root, "--artifacts", "--preset", "minimal", "--mode", "track"); e == nil {
		t.Fatal("mixed scaffold and history plans accepted")
	}
	if _, _, e := h.run("repo", "setup", h.repo.Root, "--mode", "archive"); e == nil {
		t.Fatal("history flags silently ignored")
	}
}
