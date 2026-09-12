package agenthistory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveSyncUsesExactDestinationAndRejectsDrift(t *testing.T) {
	s, _, a := fixture(t)
	remote := filepath.Join(t.TempDir(), "remote with spaces.git")
	a.Git("init", "--bare", remote)
	a.Git("remote", "add", "origin", remote)
	p, e := s.PreviewSync(t.Context(), "push", "origin", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplySync(t.Context(), p.ID); e != nil {
		t.Fatal(e)
	}
	want := a.Git("rev-parse", "HEAD")
	got, e := gitText(t.Context(), remote, "rev-parse", "refs/heads/main")
	if e != nil || got != want {
		t.Fatal("push did not preserve selected head")
	}
	a.Commit("one.md", "archive note\n", "one")
	p, e = s.PreviewSync(t.Context(), "push", "origin", "")
	if e != nil {
		t.Fatal(e)
	}
	second := filepath.Join(t.TempDir(), "other.git")
	a.Git("init", "--bare", second)
	a.Git("remote", "set-url", "origin", second)
	if _, e = s.ApplySync(t.Context(), p.ID); e == nil {
		t.Fatal("changed remote URL accepted")
	}
	if refs, e := remoteRefs(t.Context(), a.Root, second); e != nil || len(refs) != 0 {
		t.Fatal("new remote was modified")
	}
	a.Git("remote", "set-url", "origin", remote)
	p, e = s.PreviewSync(t.Context(), "push", "origin", "")
	if e != nil {
		t.Fatal(e)
	}
	a.GitIn(remote, "update-ref", "-d", "refs/heads/main")
	if _, e = s.ApplySync(t.Context(), p.ID); e == nil {
		t.Fatal("remote ref drift accepted")
	}
}

func TestMigrationBackupRefusesNonemptyRemote(t *testing.T) {
	s, r := migrationFixture(t)
	r.Commit(".specstory/history/chat.md", transcript("raw evidence\n"), "history")
	p, e := s.PreviewMigration(t.Context(), MigrationOptions{Mode: "untrack"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplyMigration(t.Context(), p.ID, ApplyOptions{WriterStopped: true}); e != nil {
		t.Fatal(e)
	}
	remote := filepath.Join(t.TempDir(), "backup.git")
	r.Git("init", "--bare", remote)
	backup, e := s.PreviewBackup(t.Context(), p.ID, remote)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplyBackup(t.Context(), backup.ID); e != nil {
		t.Fatal(e)
	}
	if got := r.GitIn(remote, "rev-parse", "refs/heads/main"); got != r.Git("rev-parse", "HEAD") {
		t.Fatal("original ref missing from backup")
	}
	if _, e = s.PreviewBackup(t.Context(), p.ID, remote); e == nil {
		t.Fatal("nonempty remote accepted")
	}
	if _, e = s.PreviewBackup(t.Context(), p.ID, "ext::dangerous-command"); e == nil {
		t.Fatal("custom remote helper accepted")
	}
}

func TestArchiveDoesNotBypassContentFilters(t *testing.T) {
	s, r, a := fixture(t)
	r.Write(".specstory/history/chat.md", transcript("evidence\n"))
	a.Commit(".gitattributes", "*.md filter=encrypted\n", "archive encryption policy")
	if _, e := s.PreviewArchive(t.Context(), ArchiveOptions{Session: "codex:" + testSession}); e == nil {
		t.Fatal("archive content filter was bypassed")
	}
}

func TestSpecStoryExternalCapturePreservesConfigComments(t *testing.T) {
	s, r, _ := fixture(t)
	r.Write(".specstory/cli/config.toml", "# keep header\n[local_sync] # keep section\noutput_dir = 'old' # keep explanation\n[cloud_sync]\nenabled = false\n")
	p, e := s.PreviewSetup(t.Context(), SetupOptions{Capture: "external", ExportIgnore: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplySetup(t.Context(), p.ID); e != nil {
		t.Fatal(e)
	}
	s, e = Open(t.Context(), s.opts)
	if e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(r.Root, ".specstory/cli/config.toml"))
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"# keep header", "# keep section", "# keep explanation", "enabled = false"} {
		if !bytesContains(data, want) {
			t.Fatal("native config comment or setting lost")
		}
	}
	if s.Binding.CaptureDir == "" {
		t.Fatal("external capture not bound")
	}
}
func bytesContains(data []byte, text string) bool {
	return len(data) >= len(text) && strings.Contains(string(data), text)
}

func TestArchivePullFastForwardsAndRetainsDivergentWork(t *testing.T) {
	s, _, a := fixture(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	a.Git("init", "--bare", remote)
	a.Git("remote", "add", "origin", remote)
	p, e := s.PreviewSync(t.Context(), "push", "origin", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplySync(t.Context(), p.ID); e != nil {
		t.Fatal(e)
	}
	other := filepath.Join(t.TempDir(), "other")
	a.Git("clone", "--branch", "main", remote, other)
	a.GitIn(other, "config", "user.name", "dev test")
	a.GitIn(other, "config", "user.email", "dev@example.test")
	a.GitIn(other, "config", "commit.gpgsign", "false")
	a.GitIn(other, "config", "core.hooksPath", t.TempDir())
	if e = os.WriteFile(filepath.Join(other, "remote.txt"), []byte("remote one\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	a.GitIn(other, "add", "remote.txt")
	a.GitIn(other, "commit", "-m", "remote one")
	a.GitIn(other, "push", "origin", "main")
	p, e = s.PreviewSync(t.Context(), "pull", "origin", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplySync(t.Context(), p.ID); e != nil {
		t.Fatal(e)
	}
	if a.Git("rev-parse", "HEAD") != a.GitIn(other, "rev-parse", "HEAD") {
		t.Fatal("pull did not advance to the reviewed commit")
	}
	a.Commit("local.txt", "local work\n", "local work")
	head := a.Git("rev-parse", "HEAD")
	if e = os.WriteFile(filepath.Join(other, "remote.txt"), []byte("remote two\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	a.GitIn(other, "add", "remote.txt")
	a.GitIn(other, "commit", "-m", "remote two")
	a.GitIn(other, "push", "origin", "main")
	p, e = s.PreviewSync(t.Context(), "pull", "origin", "")
	if e != nil {
		t.Fatal(e)
	}
	result, e := s.ApplySync(t.Context(), p.ID)
	if e == nil || result.Status != "partial" || a.Git("rev-parse", "HEAD") != head {
		t.Fatal("divergent archive was overwritten")
	}
}
