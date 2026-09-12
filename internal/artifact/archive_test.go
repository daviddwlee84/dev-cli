package artifact

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/agenthistory"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestPrepareFinalizeExternalArchiveAndIgnoredReadiness(t *testing.T) {
	isolateGitConfig(t)
	r, a := gittest.New(t), gittest.New(t)
	r.Git("config", "core.hooksPath", t.TempDir())
	a.Git("config", "core.hooksPath", t.TempDir())
	state := t.TempDir()
	h, e := agenthistory.Open(t.Context(), agenthistory.Options{Root: r.Root, StateDir: state})
	if e != nil {
		t.Fatal(e)
	}
	p, e := h.PreviewSetup(t.Context(), agenthistory.SetupOptions{Mode: "archive", Source: "specstory", Archive: a.Root, Protection: "off", ExportIgnore: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.ApplySetup(t.Context(), p.ID); e != nil {
		t.Fatal(e)
	}
	r.Git("add", ".gitignore", ".gitattributes", ".dev-cli/artifacts.toml")
	r.Git("commit", "-m", "history policy")
	head := r.Git("rev-parse", "HEAD")
	sid := "01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"
	path := filepath.Join(r.Root, ".specstory/history/chat.md")
	if e = os.MkdirAll(filepath.Dir(path), 0o700); e != nil {
		t.Fatal(e)
	}
	writeTranscript(t, path, "Codex CLI", sid, "final")
	store := NewStore(filepath.Join(state, "artifact-intents", "v1"))
	service := &Service{Store: store}
	intent, e := service.Prepare(t.Context(), PrepareRequest{Worktree: r.Root, Session: "codex:" + sid})
	if e != nil {
		t.Fatal(e)
	}
	if intent.Destination != "archive" {
		t.Fatal("new intent did not retain selected destination")
	}
	intent, e = service.Finalize(t.Context(), FinalizeRequest{IntentID: intent.ID, WriterStopped: true})
	if e != nil {
		t.Fatal(e)
	}
	if intent.ArchiveCommit == "" || intent.ArtifactCommit != "" || r.Git("rev-parse", "HEAD") != head {
		t.Fatal("archive finalization changed source history")
	}
	proof, e := InspectReadiness(t.Context(), store, r.Root)
	if e != nil || !proof.Ready() {
		t.Fatalf("archive not ready: %v %+v", e, proof)
	}
	writeTranscript(t, path, "Codex CLI", sid, "another turn")
	proof, e = InspectReadiness(t.Context(), store, r.Root)
	if e != nil || proof.Ready() {
		t.Fatal("new ignored conversation bytes became disposable")
	}
}
