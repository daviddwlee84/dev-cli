package skill

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func skillTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	return home
}

func putSkillFile(t *testing.T, dir, path string, body []byte) {
	t.Helper()
	path = filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshAbsentDoesNotInstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent", Name)
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refresh created directories: %v", err)
	}
}

func TestRefreshLegacyAndManagedOldContent(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy", false: "managed"}[legacy], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), Name)
			old := []byte("---\nname: dev-cli\ndescription: old bundled skill\n---\nOld commands\n")
			putSkillFile(t, dir, "SKILL.md", old)
			if !legacy {
				putSkillFile(t, dir, "references/obsolete.md", []byte("old reference"))
				body, _ := json.Marshal(installManifest{Version: 1, Files: map[string]string{
					"SKILL.md": digest(old), "references/obsolete.md": digest([]byte("old reference")),
				}})
				putSkillFile(t, dir, manifestName, body)
			}
			before, err := Check(dir)
			if err != nil || !before.Installed || before.Current || before.Legacy != legacy {
				t.Fatalf("before = %+v, %v", before, err)
			}
			if _, err := Refresh(dir); err != nil {
				t.Fatal(err)
			}
			after, err := Check(dir)
			if err != nil || !after.Current || after.Legacy {
				t.Fatalf("after = %+v, %v", after, err)
			}
			if !legacy {
				if _, err := os.Stat(filepath.Join(dir, "references/obsolete.md")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("obsolete managed file was retained: %v", err)
				}
			}
		})
	}
}

func TestRefreshPreservesRecordedLocalEdits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Name)
	if _, err := Install(dir, false); err != nil {
		t.Fatal(err)
	}
	putSkillFile(t, dir, "SKILL.md", []byte("my edits"))
	missing := filepath.Join(dir, "references/commands.md")
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(dir); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("refresh error = %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if string(body) != "my edits" {
		t.Fatal("local edits overwritten")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected refresh had partial writes")
	}
}

func TestInstallRejectsSymlinkDestinations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Name)
	outside := t.TempDir()
	putSkillFile(t, outside, "commands.md", []byte("foreign"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "references")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Install(dir, false); err == nil {
		t.Fatal("installed through a directory symlink")
	}
	body, _ := os.ReadFile(filepath.Join(outside, "commands.md"))
	if string(body) != "foreign" {
		t.Fatal("foreign file changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid destination caused partial writes")
	}
}

func TestUninstallPreservesExtrasAndForeignLinks(t *testing.T) {
	home := skillTestHome(t)
	dir := DefaultDir()
	link := filepath.Join(home, ".claude", "skills", Name)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	if err := os.Symlink(foreign, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Install(dir, true); err != nil {
		t.Fatal(err)
	}
	putSkillFile(t, dir, "personal.md", []byte("keep me"))
	plan, err := PlanUninstall(dir)
	if err != nil || len(plan.Links) != 0 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if err := ApplyUninstall(plan); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "personal.md")); string(body) != "keep me" {
		t.Fatal("unrelated file was removed")
	}
	if target, _ := os.Readlink(link); target != foreign {
		t.Fatal("foreign link was changed")
	}
}

func TestUninstallStalePreviewAndOwnedLink(t *testing.T) {
	home := skillTestHome(t)
	dir := DefaultDir()
	link := filepath.Join(home, ".claude", "skills", Name)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, true); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	plan, err := PlanUninstall(dir)
	if err != nil || len(plan.Links) != 1 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	putSkillFile(t, dir, "SKILL.md", []byte("new edits"))
	if err := ApplyUninstall(plan); err == nil {
		t.Fatal("stale preview accepted")
	}
	if _, err := os.Readlink(link); err != nil {
		t.Fatal("rejected uninstall removed the agent link")
	}
	if _, err := Install(dir, false); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanUninstall(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUninstall(plan); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, link} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("uninstall left %s: %v", path, err)
		}
	}
}

func TestUninstallRejectsLegacyAndTraversalManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Name)
	putSkillFile(t, dir, "SKILL.md", []byte("legacy"))
	if _, err := PlanUninstall(dir); err == nil {
		t.Fatal("legacy uninstall accepted without ownership")
	}
	body, _ := json.Marshal(installManifest{Version: 1, Files: map[string]string{
		"SKILL.md": digest([]byte("legacy")), "../other": digest(nil),
	}})
	putSkillFile(t, dir, manifestName, body)
	if _, err := PlanUninstall(dir); err == nil {
		t.Fatal("traversing manifest accepted")
	}
}
