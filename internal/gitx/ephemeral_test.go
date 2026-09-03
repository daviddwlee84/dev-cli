package gitx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestInspectEphemeralCheckoutCountsIgnoredAndUntrackedWithoutPaths(t *testing.T) {
	r := gittest.New(t)
	r.Write(".gitignore", "ignored/\n")
	r.Git("add", ".gitignore")
	r.Git("commit", "-m", "test: ignore fixture")

	clean, err := InspectEphemeralCheckout(t.Context(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if clean.Status.Dirty() || clean.Ignored != 0 || clean.DirtySubmodules != 0 || clean.Fingerprint == "" {
		t.Fatalf("clean inspection = %+v", clean)
	}

	if err := os.MkdirAll(filepath.Join(r.Root, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Root, "ignored", "secret.env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignored, err := InspectEphemeralCheckout(t.Context(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if ignored.Status.Dirty() || ignored.Ignored != 1 || ignored.Fingerprint == clean.Fingerprint {
		t.Fatalf("ignored inspection = %+v", ignored)
	}

	if err := os.WriteFile(filepath.Join(r.Root, "untracked.txt"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}
	untracked, err := InspectEphemeralCheckout(t.Context(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if untracked.Status.Untracked != 1 || !untracked.Status.Dirty() || untracked.Ignored != 1 || untracked.Fingerprint == ignored.Fingerprint {
		t.Fatalf("untracked inspection = %+v", untracked)
	}
}

func TestInspectEphemeralCheckoutRecursesIntoSubmodules(t *testing.T) {
	child := gittest.New(t)
	child.Write(".gitignore", "ignored.env\n")
	child.Git("add", ".gitignore")
	child.Git("commit", "-m", "test: ignore submodule fixture")
	parent := gittest.New(t)
	parent.Git("-c", "protocol.file.allow=always", "submodule", "add", child.Root, "modules/child")
	parent.Git("commit", "-am", "test: add submodule")

	clean, err := InspectEphemeralCheckout(t.Context(), parent.Root)
	if err != nil {
		t.Fatal(err)
	}
	if clean.Ignored != 0 || clean.DirtySubmodules != 0 {
		t.Fatalf("clean submodule inspection = %+v", clean)
	}
	childPath := filepath.Join(parent.Root, "modules", "child")
	if err := os.WriteFile(filepath.Join(childPath, "ignored.env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignored, err := InspectEphemeralCheckout(t.Context(), parent.Root)
	if err != nil {
		t.Fatal(err)
	}
	if ignored.Ignored != 1 || ignored.DirtySubmodules == 0 || ignored.Fingerprint == clean.Fingerprint {
		t.Fatalf("ignored submodule inspection = %+v", ignored)
	}
}

func TestInProgressRecognizesBisectState(t *testing.T) {
	r := gittest.New(t)
	if err := os.WriteFile(filepath.Join(r.Root, ".git", "BISECT_LOG"), []byte("git bisect start\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation, active, err := InProgress(r.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !active || operation != "BISECT_LOG" {
		t.Fatalf("InProgress = %q, %v", operation, active)
	}
}

func TestSubmoduleDirtyCountFromPorcelain(t *testing.T) {
	raw := "# branch.oid deadbeef\x00" +
		"1 .M S.MU 160000 160000 160000 abcdef abcdef vendor/module\x00" +
		"1 M. N... 100644 100644 100644 abcdef abcdef ordinary.txt\x00"
	if got := submoduleDirtyCount(raw); got != 1 {
		t.Fatalf("submodule dirty count = %d, want 1", got)
	}
}
