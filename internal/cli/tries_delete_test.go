package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTryDeleteCLIRequiresExactPermanentApproval(t *testing.T) {
	h := newHarness(t)
	h.mustRun("try", "discard", "--no-git")
	var rows []struct {
		Identity struct {
			ID string `json:"id"`
		} `json:"identity"`
		Live struct {
			Path string `json:"path"`
		} `json:"live"`
	}
	if err := json.Unmarshal([]byte(h.mustRun("tries", "list", "--json")), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("%+v %v", rows, err)
	}
	id, path := rows[0].Identity.ID, rows[0].Live.Path
	args := []string{"tries", "delete", id, "--permanent", "--assume-no-runtime"}
	if _, _, err := h.run(append(args, "--yes")...); err == nil {
		t.Fatal("--yes authorized permanent deletion")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	preview := h.mustRun(append(args, "--dry-run", "--json")...)
	if !json.Valid([]byte(preview)) {
		t.Fatalf("not JSON: %s", preview)
	}
	if _, _, err := h.run(append(args, "--confirm-delete", "wrong")...); err == nil {
		t.Fatal("wrong ID accepted")
	}
	h.mustRun(append(args, "--confirm-delete", id)...)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source remained: %v", err)
	}
	history := h.mustRun("tries", "list", "--all", "--json")
	if !strings.Contains(history, id) || !strings.Contains(history, "evicted") || !strings.Contains(history, "permanent") {
		t.Fatal(history)
	}
}

func TestBrowseCLIPrintFromCheckoutAndLinkedWorktree(t *testing.T) {
	h := newHarness(t)
	h.repo.Git("remote", "add", "origin", "git@github.com:owner/project.git")
	path := filepath.Join(h.home, "linked")
	h.repo.Git("worktree", "add", "-b", "browse-test", path, "main")
	for _, dir := range []string{h.repo.Root, path} {
		t.Chdir(dir)
		if out := h.mustRun("browse", "--print"); strings.TrimSpace(out) != "https://github.com/owner/project" {
			t.Fatal(out)
		}
		if out := h.mustRun("repo", "browse", "--print"); strings.TrimSpace(out) != "https://github.com/owner/project" {
			t.Fatal(out)
		}
	}
}
