package agenthistory

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

const testSession = "01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2"

func fixture(t *testing.T) (*Service, *gittest.Repo, *gittest.Repo) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r, a := gittest.New(t), gittest.New(t)
	empty := t.TempDir()
	r.Git("config", "core.hooksPath", empty)
	a.Git("config", "core.hooksPath", empty)
	s, e := Open(t.Context(), Options{Root: r.Root, StateDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	r.Write(".gitattributes", "* text=auto eol=lf\n")
	r.Write(".gitignore", "# user rules\n/build\n")
	p, e := s.PreviewSetup(t.Context(), SetupOptions{Mode: "archive", Source: "specstory", Archive: a.Root, Protection: "off", ExportIgnore: true})
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
	return s, r, a
}
func transcript(body string) string {
	return "# Evidence\n<!-- Codex CLI Session " + testSession + " (2026-09-12) -->\n" + body
}

func TestArchiveOffPreservesBytesAndSourceIndex(t *testing.T) {
	s, r, a := fixture(t)
	data := transcript(strings.Repeat("history line\r\n", 1<<21))
	r.Write(".specstory/history/chat.md", data)
	r.Write("code.go", "package example\n")
	r.Git("add", "code.go")
	r.Write("code.go", "package edited\n")
	index, head := r.Git("write-tree"), r.Git("rev-parse", "HEAD")
	p, e := s.PreviewArchive(t.Context(), ArchiveOptions{Session: "codex:" + testSession})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Files) != 1 || p.Files[0].Scan != "not-scanned" {
		t.Fatal("off protection ran scanning")
	}
	if _, e = s.ApplyArchive(t.Context(), p.ID, ApplyOptions{}); e == nil {
		t.Fatal("missing writer proof accepted")
	}
	completed, e := s.ApplyArchive(t.Context(), p.ID, ApplyOptions{WriterStopped: true})
	if e != nil {
		t.Fatal(e)
	}
	if completed.Status != "complete" || completed.ArchiveCommit == "" {
		t.Fatal("missing durable result")
	}
	if r.Git("write-tree") != index || r.Git("rev-parse", "HEAD") != head {
		t.Fatal("source Git was changed")
	}
	b, _ := os.ReadFile(filepath.Join(r.Root, ".specstory/history/chat.md"))
	if string(b) != data {
		t.Fatal("source bytes changed")
	}
	got, e := s.Find(t.Context(), Query{Session: "codex:" + testSession, Text: "history line"})
	if e != nil || len(got) != 1 {
		t.Fatalf("find: %v %d", e, len(got))
	}
	stored, e := runGit(t.Context(), a.Root, nil, MaxFileBytes, "cat-file", "blob", "HEAD:"+got[0].Record.Content)
	if e != nil || !bytes.Equal(stored, []byte(data)) {
		t.Fatal("archive bytes differ")
	}
	ready, e := s.Readiness(t.Context())
	if e != nil || !ready.Ready {
		t.Fatalf("readiness: %+v %v", ready, e)
	}
	before := a.Git("rev-parse", "HEAD")
	if _, e = s.ApplyArchive(t.Context(), p.ID, ApplyOptions{WriterStopped: true}); e != nil || a.Git("rev-parse", "HEAD") != before {
		t.Fatal("repeat apply was not idempotent")
	}
	r.Write(".specstory/history/chat.md", data+"new turn\n")
	ready, e = s.Readiness(t.Context())
	if e != nil || ready.Ready {
		t.Fatal("new ignored bytes became disposable")
	}
}

func TestArchiveRejectsChangedSourceAndTamperedPlan(t *testing.T) {
	s, r, a := fixture(t)
	r.Write(".specstory/history/chat.md", transcript("first\n"))
	head := a.Git("rev-parse", "HEAD")
	p, e := s.PreviewArchive(t.Context(), ArchiveOptions{Session: "codex:" + testSession})
	if e != nil {
		t.Fatal(e)
	}
	r.Write(".specstory/history/chat.md", transcript("second\n"))
	if _, e = s.ApplyArchive(t.Context(), p.ID, ApplyOptions{WriterStopped: true}); e == nil || a.Git("rev-parse", "HEAD") != head {
		t.Fatal("stale source applied")
	}
	p, e = s.PreviewArchive(t.Context(), ArchiveOptions{Session: "codex:" + testSession})
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(s.planDir(p.ID), "plan.json")
	raw, _ := os.ReadFile(path)
	var record map[string]any
	_ = json.Unmarshal(raw, &record)
	record["Root"] = "elsewhere"
	if e = os.WriteFile(path, encode(record), 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplyArchive(t.Context(), p.ID, ApplyOptions{WriterStopped: true}); e == nil {
		t.Fatal("modified plan accepted")
	}
}

func TestSetupPreservesUserRulesAndUnmanagedHasNoArchive(t *testing.T) {
	s, r, _ := fixture(t)
	attributes, _ := os.ReadFile(filepath.Join(r.Root, ".gitattributes"))
	ignore, _ := os.ReadFile(filepath.Join(r.Root, ".gitignore"))
	if !strings.Contains(string(attributes), "* text=auto eol=lf") || !strings.Contains(string(ignore), "# user rules\n/build") {
		t.Fatal("user rules lost")
	}
	p, e := s.PreviewSetup(t.Context(), SetupOptions{Mode: "unmanaged", Source: "files", Paths: []string{"notes"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplySetup(t.Context(), p.ID); e != nil {
		t.Fatal(e)
	}
	ignore, _ = os.ReadFile(filepath.Join(r.Root, ".gitignore"))
	if strings.Contains(string(ignore), ".specstory/history") {
		t.Fatal("owned ignore was not removed")
	}
}

func TestArchiveRedactsCopyAndRetainsOriginal(t *testing.T) {
	s, r, _ := fixture(t)
	r.Write(".dev-cli/hygiene.toml", "version = 1\nsecrets = 'off'\nknown = 'block'\ngeneric = 'off'\n[[rules]]\nid = 'private-host'\nkind = 'literal'\nvalue = 'private-host-unique'\nreplacement = 'host.example.invalid'\n")
	p, e := s.PreviewSetup(t.Context(), SetupOptions{Protection: "redact", ExportIgnore: true})
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
	r.Write(".specstory/history/chat.md", transcript("private-host-unique\n"))
	p, e = s.PreviewArchive(t.Context(), ArchiveOptions{Session: "codex:" + testSession})
	if e != nil {
		t.Fatal(e)
	}
	if p.Files[0].Replacements != 1 {
		t.Fatal("no copy redaction")
	}
	if _, e = s.ApplyArchive(t.Context(), p.ID, ApplyOptions{WriterStopped: true}); e != nil {
		t.Fatal(e)
	}
	data, _ := os.ReadFile(filepath.Join(r.Root, ".specstory/history/chat.md"))
	if !bytes.Contains(data, []byte("private-host-unique")) {
		t.Fatal("source redacted")
	}
	matches, e := s.Find(t.Context(), Query{Text: "host.example.invalid"})
	if e != nil || len(matches) != 1 {
		t.Fatal("sanitized copy missing")
	}
	record, e := s.load(t.Context(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(s.planDir(p.ID), record.Snapshots[0].Payload+".original")); e != nil {
		t.Fatal(e)
	}
	ready, e := s.Readiness(t.Context())
	if e != nil || ready.Ready {
		t.Fatal("lost original recovery permitted cleanup")
	}
}

func TestSetupRejectsChangedPrivateProposal(t *testing.T) {
	s, _, _ := fixture(t)
	p, e := s.PreviewSetup(t.Context(), SetupOptions{Mode: "unmanaged", Source: "files", Paths: []string{"notes"}})
	if e != nil {
		t.Fatal(e)
	}
	record, e := s.load(t.Context(), p.ID)
	if e != nil || len(record.Changes) == 0 {
		t.Fatal("missing setup changes")
	}
	if e = os.WriteFile(filepath.Join(s.planDir(p.ID), record.Changes[0].Desired), []byte("changed proposal"), 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplySetup(t.Context(), p.ID); e == nil {
		t.Fatal("modified setup payload accepted")
	}
}

func TestReadinessDoesNotAcquireArchiveMutationLock(t *testing.T) {
	s, r, _ := fixture(t)
	r.Write(".specstory/history/chat.md", transcript("fixed evidence"))
	p, e := s.PreviewArchive(t.Context(), ArchiveOptions{Session: "codex:" + testSession})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplyArchive(t.Context(), p.ID, ApplyOptions{WriterStopped: true}); e != nil {
		t.Fatal(e)
	}
	lease, e := lockx.AcquireDir(t.Context(), s.Dir, "test held archive mutation")
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	proof, e := s.Readiness(ctx)
	if e != nil || !proof.Ready {
		t.Fatalf("read-only readiness took mutation lock: %v", e)
	}
}

func TestSessionHandoffExactPathBinding(t *testing.T) {
	s, r, _ := fixture(t)
	data := "<!-- Generated by SpecStory, Markdown v2.1.0 -->\n" + transcript("selected\n")
	r.Write(".specstory/history/selected.md", data)
	r.Write(".specstory/history/alias.md", data)
	selected := filepath.Join(s.Root, ".specstory/history/selected.md")
	alias := filepath.Join(s.Root, ".specstory/history/alias.md")
	session := "codex:" + testSession
	if _, err := s.LocateSession(t.Context(), session); err == nil {
		t.Fatal("ambiguous session selected")
	}
	if got, err := s.LocateSession(t.Context(), session, selected); err != nil || got != selected {
		t.Fatalf("exact selection=%q %v", got, err)
	}
	if _, err := s.LocateSession(t.Context(), "claude:"+testSession, selected); err == nil {
		t.Fatal("wrong provider selected")
	}
	p, err := s.PreviewArchive(t.Context(), ArchiveOptions{Session: session, SpecStoryPath: alias})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckSessionPlan(t.Context(), p.ID, session, selected); err == nil {
		t.Fatal("same-UUID alternate alias substituted for prepared path")
	}
	if err := s.CheckSessionPlan(t.Context(), p.ID, session, alias); err != nil {
		t.Fatal(err)
	}
	// Old handoffs retain session-only compatibility when no selector exists.
	if err := s.CheckSessionPlan(t.Context(), p.ID, session); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LocateSession(t.Context(), session, selected); err == nil {
		t.Fatal("missing exact path fell back to alias")
	}
}

func TestSessionHandoffRejectsUnprovenSelectedPath(t *testing.T) {
	for _, kind := range []string{"body UUID", "symlink", "escape", "wrong session"} {
		t.Run(kind, func(t *testing.T) {
			s, r, _ := fixture(t)
			data := "<!-- Generated by SpecStory, Markdown v2.1.0 -->\n" + transcript("body\n")
			r.Write(".specstory/history/selected.md", data)
			path := filepath.Join(s.Root, ".specstory/history/selected.md")
			switch kind {
			case "body UUID":
				r.Write(".specstory/history/selected.md", "<!-- Generated by SpecStory, Markdown v2.1.0 -->\n# Title\nbody\n<!-- Codex CLI Session "+testSession+" (now) -->\n")
			case "symlink":
				link := filepath.Join(s.Root, ".specstory/history/link.md")
				if err := os.Symlink(path, link); err != nil {
					t.Skip(err)
				}
				path = link
			case "escape":
				path = filepath.Join(t.TempDir(), "outside.md")
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			case "wrong session":
				r.Write(".specstory/history/selected.md", strings.ReplaceAll(data, testSession, "72b5c55e-d964-45cd-b040-cb29d0d7af05"))
			}
			if _, err := s.LocateSession(t.Context(), "codex:"+testSession, path); err == nil {
				t.Fatal("unproven selected path accepted")
			}
		})
	}
}

func TestSessionSelectionUsesExternalCaptureRoot(t *testing.T) {
	s, r, a := fixture(t)
	plan, err := s.PreviewSetup(t.Context(), SetupOptions{Mode: "archive", Source: "specstory", Capture: "external", Archive: a.Root, Protection: "off", ExportIgnore: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplySetup(t.Context(), plan.ID); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), s.opts)
	if err != nil {
		t.Fatal(err)
	}
	data := "<!-- Generated by SpecStory, Markdown v2.1.0 -->\n" + transcript("external capture\n")
	selected := filepath.Join(s.Binding.CaptureDir, "selected.md")
	if err = os.WriteFile(selected, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.Binding.CaptureDir, "alias.md"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LocateSession(t.Context(), "codex:"+testSession, "selected.md"); err != nil || got != selected {
		t.Fatalf("external exact selection=%q %v", got, err)
	}
	r.Write(".specstory/history/selected.md", data)
	if _, err := s.LocateSession(t.Context(), "codex:"+testSession, filepath.Join(r.Root, ".specstory/history/selected.md")); err == nil {
		t.Fatal("checkout alias substituted for external capture selection")
	}
	p, err := s.PreviewArchive(t.Context(), ArchiveOptions{Session: "codex:" + testSession, SpecStoryPath: selected})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CheckSessionPlan(t.Context(), p.ID, "codex:"+testSession, selected); err != nil {
		t.Fatal(err)
	}
}
