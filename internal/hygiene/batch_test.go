package hygiene

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func batchDependencies(t *testing.T) {
	t.Helper()
	for _, name := range []string{"gitleaks", "pre-commit"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " unavailable")
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
}
func batchRepository(t *testing.T) *gittest.Repo {
	t.Helper()
	r := gittest.New(t)
	put(t, r.Root, ".hooks/pre-commit", "#!/bin/sh\nexec pre-commit run --config .pre-commit-config.yaml \"$@\"\n")
	if err := os.Chmod(filepath.Join(r.Root, ".hooks/pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git("config", "core.hooksPath", ".hooks")
	return r
}

func TestBatchSetupSelectedOnlyAndStaleRepositoryRetained(t *testing.T) {
	batchDependencies(t)
	left, right := batchRepository(t), batchRepository(t)
	state := t.TempDir()
	options := []Options{{Root: left.Root, StateDir: state}, {Root: right.Root, StateDir: state}, {Root: left.Root, StateDir: state}}
	batch, err := PreviewSetupBatch(t.Context(), options, SetupOptions{MigrateHooks: true, UpdateRules: true})
	if err != nil || len(batch.ops) != 2 || batch.Entries[2].Status != "duplicate" {
		t.Fatalf("preview: %+v %v", batch.Entries, err)
	}
	put(t, right.Root, ".pre-commit-config.yaml", "repos: [] # another writer\n")
	receipt, err := ApplySetupBatch(t.Context(), batch, []string{batch.ops[0].planID, batch.ops[1].planID}, ApplyOptions{})
	if err == nil || receipt.Outcomes[0].Status != "completed" || receipt.Outcomes[1].Status != "stale" {
		t.Fatalf("outcomes: %+v %v", receipt.Outcomes, err)
	}
	got, _ := os.ReadFile(filepath.Join(right.Root, ".pre-commit-config.yaml"))
	if !strings.Contains(string(got), "another writer") {
		t.Fatal("stale repo overwritten")
	}
	if _, err := os.Stat(receipt.ReceiptPath); err != nil {
		t.Fatal("missing private receipt")
	}
	// A fresh preview of the completed repository has no file changes.
	next, err := PreviewSetupBatch(t.Context(), options[:1], SetupOptions{MigrateHooks: true, UpdateRules: true})
	if err != nil || next.Entries[0].Plan == nil || len(next.Entries[0].Plan.Files) != 0 {
		t.Fatalf("non-idempotent preview: %+v %v", next.Entries, err)
	}
}

func TestBatchRejectsChangedPreviewAndCancelsWithoutWrites(t *testing.T) {
	batchDependencies(t)
	r := batchRepository(t)
	batch, err := PreviewSetupBatch(t.Context(), []Options{{Root: r.Root, StateDir: t.TempDir()}}, SetupOptions{})
	if err != nil || len(batch.ops) != 1 {
		t.Fatal(batch, err)
	}
	id := batch.ops[0].planID
	batch.Entries[0].Root += "changed"
	if _, err := ApplySetupBatch(t.Context(), batch, []string{id}, ApplyOptions{}); !errors.Is(err, ErrStale) {
		t.Fatal("changed public preview accepted")
	}
	batch.Entries[0].Root = r.Root
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ApplySetupBatch(ctx, batch, []string{id}, ApplyOptions{}); err == nil {
		t.Fatal("canceled batch succeeded")
	}
	if _, err := os.Stat(filepath.Join(r.Root, ".pre-commit-config.yaml")); !os.IsNotExist(err) {
		t.Fatal("canceled operation wrote configuration")
	}
}

func TestSharedHookChecksSiblingBeforeInstallationAndApply(t *testing.T) {
	batchDependencies(t)
	r := gittest.New(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	r.Git("worktree", "add", "-b", "sibling", sibling, "HEAD")
	s, err := Open(t.Context(), Options{Root: r.Root, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewSetup(t.Context(), false); err == nil {
		t.Fatal("shared hook could break an unconfigured sibling")
	}
	put(t, sibling, ".pre-commit-config.yaml", "repos: []\n")
	plan, err := s.PreviewSetup(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	put(t, sibling, ".pre-commit-config.yaml", "repos: [] # changed\n")
	if _, err := s.Apply(t.Context(), plan.ID, ApplyOptions{}); !errors.Is(err, ErrStale) {
		t.Fatalf("sibling change not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Root, ".pre-commit-config.yaml")); !os.IsNotExist(err) {
		t.Fatal("stale shared-hook plan changed root")
	}
}

func TestHookMigrationKeepsFinalizerAndRejectsCustomScanner(t *testing.T) {
	input := "# retain this comment\nrepos:\n  - repo: https://github.com/gitleaks/gitleaks\n    rev: v8.30.1\n    hooks:\n      - id: gitleaks-system\n  - repo: local\n    hooks:\n      - id: check-agent-artifact-secrets\n        entry: artifact-checker\n        language: system\n"
	data, notes, err := migrateScannerHooks([]byte(input))
	if err != nil || strings.Contains(string(data), "gitleaks-system") || !strings.Contains(string(data), "artifact-checker") || !strings.Contains(string(data), "retain this comment") || len(notes) != 2 {
		t.Fatalf("migration failed: %s %v", data, err)
	}
	legacy := strings.Replace(input, "check-agent-artifact-secrets", "redact-agent-secrets", 1)
	if _, _, err := migrateScannerHooks([]byte(legacy)); err == nil {
		t.Fatal("unknown mutating redactor accepted as a validation hook")
	}
	custom := strings.Replace(input, "      - id: gitleaks-system", "      - id: gitleaks-system\n        args: [--custom]", 1)
	if _, _, err := migrateScannerHooks([]byte(custom)); err == nil {
		t.Fatal("custom scanner removed")
	}
}

func TestKnownRuleMigrationPreservesCustomRulesAndComments(t *testing.T) {
	input := "# user comment\n[extend]\nuseDefault = true\n\n[[rules]]\nid = \"generic-password-assignment\"\nregex = '''" + legacyPasswordRegex + "'''\nsecretGroup = 1\n\n[[rules]]\nid = \"my-custom\"\nregex = '''custom-value'''\n"
	updated, changed, _, err := updateKnownPasswordRule([]byte(input))
	if err != nil || !changed || !strings.Contains(string(updated), "# user comment") || !strings.Contains(string(updated), "my-custom") || !strings.Contains(string(updated), "generic-password-assignment-unquoted") {
		t.Fatal("custom rules lost", err)
	}
	if _, changed, _, err = updateKnownPasswordRule(updated); err != nil || changed {
		t.Fatal("migration was not idempotent", err)
	}
	custom := strings.Replace(input, "secretGroup = 1", "secretGroup = 1\npath = 'secrets-only'", 1)
	if result, changed, _, err := updateKnownPasswordRule([]byte(custom)); err != nil || changed || string(result) != custom {
		t.Fatal("custom scoped rule overwritten")
	}
}
