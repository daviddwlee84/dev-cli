package hygiene

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactPathClassificationCoversPortableAliases(t *testing.T) {
	for _, path := range []string{
		".specstory/history/session.md", ".SPECSTORY/history/session.md", ".CoDeX/session.jsonl",
		".CLAUDE/plans/task.md", ".CuRsOr/plan.md", ".OpenCode/plan.md", ".SPECIFY/spec.md",
		".specstory./history/session.md", ".specstory /history/session.md", ".codex... /session.jsonl",
		`.SPECSTORY\history\session.md`, "member/.SpEcStOrY/history/session.md", ".CLAUDE",
		".ſpecstory/history/session.md",
	} {
		if !IsArtifactPath(path) {
			t.Errorf("artifact alias not protected: %q", path)
		}
	}
	for _, path := range []string{"", "notes.md", "src/.specstory.md", ".specstory-other/session.md", "codex/session.jsonl"} {
		if IsArtifactPath(path) {
			t.Errorf("ordinary path classified as artifact: %q", path)
		}
	}
}

func TestEncodingRepairArtifactAliasesRequireWriterAttestation(t *testing.T) {
	for _, name := range []string{".SPECSTORY/history/session.md", ".CoDeX/session.jsonl", "member/.CLAUDE/plans/task.md"} {
		t.Run(name, func(t *testing.T) {
			service, repo := testService(t)
			before := "unfinished writer\x80"
			put(t, repo.Root, name, before)
			plan, err := service.PreviewRepairEncoding(t.Context(), []string{name}, "replace")
			if err != nil || !plan.RequiresWriterStopped {
				t.Fatalf("artifact alias lacks writer guard: %+v %v", plan, err)
			}
			if _, err = service.Apply(t.Context(), plan.ID, ApplyOptions{}); err == nil {
				t.Fatal("artifact alias applied without writer attestation")
			}
			content, err := os.ReadFile(filepath.Join(repo.Root, filepath.FromSlash(name)))
			if err != nil || string(content) != before {
				t.Fatalf("rejected apply changed source: %q %v", content, err)
			}
			applied, err := service.Apply(t.Context(), plan.ID, ApplyOptions{WriterStopped: true})
			if err != nil || applied.Status != "applied" {
				t.Fatalf("attested repair failed: %+v %v", applied, err)
			}
			if _, err = service.Restore(t.Context(), applied.Recovery[0], true, ApplyOptions{}); err == nil {
				t.Fatal("artifact alias restored without writer attestation")
			}
		})
	}
}

func TestEncodingRepairRejectsWin32NormalizedPathComponents(t *testing.T) {
	service, repo := testService(t)
	put(t, repo.Root, ".specstory/history/session.md", "unfinished\x80")
	for _, name := range []string{".specstory./history/session.md", ".specstory /history/session.md", ".specstory/history./session.md", ".specstory/history/session.md.", "notes.md ", "member/.GIT ./config", "SPECS~1/history/session.md", "member/CODEX~12/session.txt"} {
		if _, err := service.PreviewRepairEncoding(t.Context(), []string{name}, "replace"); err == nil {
			t.Errorf("ambiguous path accepted: %q", name)
		}
	}
	content, err := os.ReadFile(filepath.Join(repo.Root, ".specstory", "history", "session.md"))
	if err != nil || string(content) != "unfinished\x80" {
		t.Fatalf("path refusal changed canonical source: %q %v", content, err)
	}
}

func TestEncodingRepairOlderSignedPlanCannotBypassArtifactAttestation(t *testing.T) {
	service, repo := testService(t)
	name := ".SPECSTORY/history/session.md"
	put(t, repo.Root, name, "unfinished\x80")
	plan, err := service.PreviewRepairEncoding(t.Context(), []string{name}, "replace")
	if err != nil {
		t.Fatal(err)
	}
	var saved planRecord
	if err := service.load(t.Context(), plan.ID, &saved); err != nil {
		t.Fatal(err)
	}
	// Model a correctly signed plan from the earlier case-sensitive classifier.
	saved.Plan.RequiresWriterStopped = false
	saved.Plan.Revision = service.planRevision(saved)
	if err := service.save(t.Context(), plan.ID, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(t.Context(), plan.ID, ApplyOptions{}); err == nil {
		t.Fatal("older signed artifact plan applied without current writer guard")
	}
	content, err := os.ReadFile(filepath.Join(repo.Root, filepath.FromSlash(name)))
	if err != nil || string(content) != "unfinished\x80" {
		t.Fatalf("rejected plan changed source: %q %v", content, err)
	}
}
