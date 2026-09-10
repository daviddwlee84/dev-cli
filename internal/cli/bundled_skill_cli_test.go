package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/skill"
)

func TestBundledSkillInstallCheckRefreshAndUninstall(t *testing.T) {
	h := newHarness(t)
	dir := skill.DefaultDir()
	h.mustRun("skill", "install", "--if-installed")
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("conditional install created an absent skill")
	}
	h.mustRun("skill", "install", "--no-link")
	h.mustRun("skill", "install", "--check")
	if err := os.Remove(filepath.Join(dir, "references/commands.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.run("skill", "install", "--check"); err == nil {
		t.Fatal("drift check passed with missing content")
	}
	h.mustRun("skill", "install", "--if-installed")
	h.mustRun("skill", "install", "--check")
	h.mustRun("skill", "uninstall", "--dry-run")
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal("preview removed the installed skill")
	}
	h.mustRun("skill", "uninstall", "--yes")
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uninstall left the installation: %v", err)
	}
	h.mustRun("skill", "uninstall", "--yes")
}
