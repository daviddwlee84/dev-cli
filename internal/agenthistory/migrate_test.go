package agenthistory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func migrationFixture(t *testing.T) (*Service, *gittest.Repo) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r := gittest.New(t)
	r.Git("config", "core.hooksPath", t.TempDir())
	s, e := Open(t.Context(), Options{Root: r.Root, StateDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	return s, r
}

func TestMigrationSplitPreservesOriginalAndExtractsDeletedHistory(t *testing.T) {
	if _, e := exec.LookPath("git-filter-repo"); e != nil {
		t.Skip("git-filter-repo is required for the isolated migration integration test")
	}
	s, r := migrationFixture(t)
	r.Commit(".specstory/history/old.md", transcript("earlier conversation\n"), "first history")
	r.Git("tag", "v0.0.1")
	r.Commit(".specstory/history/old.md", transcript("later conversation\n"), "history update")
	r.Commit("code.txt", "product\n", "product work")
	r.Git("rm", ".specstory/history/old.md")
	r.Git("commit", "-m", "remove old transcript")
	head, tag := r.Git("rev-parse", "HEAD"), r.Git("rev-parse", "v0.0.1")
	p, e := s.PreviewMigration(t.Context(), MigrationOptions{Mode: "split"})
	if e != nil {
		t.Fatal(e)
	}
	result, e := s.ApplyMigration(t.Context(), p.ID, ApplyOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if result.Status != "complete" {
		t.Fatalf("result: %+v", result)
	}
	if r.Git("rev-parse", "HEAD") != head || r.Git("rev-parse", "v0.0.1") != tag {
		t.Fatal("source refs changed")
	}
	filtered := filepath.Join(result.Output, "filtered.git")
	history := filepath.Join(result.Output, "history.git")
	if paths, e := gitText(t.Context(), filtered, "log", "--all", "--format=", "--name-only", "--", ".specstory/history"); e != nil || paths != "" {
		t.Fatalf("history remains in filtered tree: %v", e)
	}
	if body, e := gitText(t.Context(), history, "show", "v0.0.1:.specstory/history/old.md"); e != nil || !strings.Contains(body, "earlier conversation") {
		t.Fatalf("historical evidence missing: %v", e)
	}
	if code, e := gitText(t.Context(), filtered, "show", "HEAD:code.txt"); e != nil || code != "product" {
		t.Fatalf("product changed: %v", e)
	}
	for _, path := range []string{"original.bundle", "original.git", "history.git", "filtered.git", "filtered-commit-map", "history-commit-map", "migration.json"} {
		if _, e = os.Stat(filepath.Join(result.Output, path)); e != nil {
			t.Fatal(e)
		}
	}
}

func TestMigrationUntrackKeepsFilesAndUnrelatedIndex(t *testing.T) {
	s, r := migrationFixture(t)
	r.Commit(".specstory/history/chat.md", transcript("committed version\n"), "history")
	r.Write(".specstory/history/chat.md", transcript("final version\n"))
	r.Write("code.txt", "staged code\n")
	r.Git("add", "code.txt")
	r.Write("code.txt", "working code\n")
	before, head := r.Git("rev-parse", ":code.txt"), r.Git("rev-parse", "HEAD")
	p, e := s.PreviewMigration(t.Context(), MigrationOptions{Mode: "untrack"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplyMigration(t.Context(), p.ID, ApplyOptions{}); e == nil {
		t.Fatal("untrack accepted absent writer proof")
	}
	result, e := s.ApplyMigration(t.Context(), p.ID, ApplyOptions{WriterStopped: true})
	if e != nil {
		t.Fatal(e)
	}
	if r.Git("rev-parse", "HEAD") != head || r.Git("rev-parse", ":code.txt") != before {
		t.Fatal("unrelated index or source commit changed")
	}
	if got := r.Git("ls-files", "--", ".specstory/history"); got != "" {
		t.Fatal("history still tracked")
	}
	for _, path := range []string{filepath.Join(r.Root, ".specstory/history/chat.md"), filepath.Join(result.Output, "current/.specstory/history/chat.md")} {
		b, e := os.ReadFile(path)
		if e != nil || !strings.Contains(string(b), "final version") {
			t.Fatal("current evidence lost")
		}
	}
	if got := r.Git("check-ignore", ".specstory/history/chat.md"); got != ".specstory/history/chat.md" {
		t.Fatal("history not ignored")
	}
}

func TestMigrationRefDriftAndExistingOutputRefuseBeforeMutation(t *testing.T) {
	s, r := migrationFixture(t)
	r.Commit(".specstory/history/chat.md", transcript("one\n"), "history")
	if _, e := s.PreviewMigration(t.Context(), MigrationOptions{Mode: "untrack", Output: r.Root}); e == nil {
		t.Fatal("existing source output accepted")
	}
	p, e := s.PreviewMigration(t.Context(), MigrationOptions{Mode: "untrack"})
	if e != nil {
		t.Fatal(e)
	}
	r.Git("branch", "another")
	if _, e = s.ApplyMigration(t.Context(), p.ID, ApplyOptions{WriterStopped: true}); e == nil {
		t.Fatal("new ref did not invalidate plan")
	}
	if _, e = os.Stat(p.Output); !os.IsNotExist(e) {
		t.Fatal("stale plan created output")
	}
}
