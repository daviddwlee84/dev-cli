package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
)

// fakeHome builds a HOME with the given repo layout and points $HOME at it.
func fakeHome(t *testing.T, repos ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("GHQ_ROOT", "")
	t.Setenv("TRY_PATH", "")
	t.Setenv("PATH", "")
	for _, rel := range repos {
		dir := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestDetectAddsChezmoiSourceAsExactRepository(t *testing.T) {
	home := fakeHome(t, "code/one")
	source := filepath.Join(home, ".local", "share", "chezmoi")
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	layout := config.DetectLayout()
	if len(layout.RepoPaths) != 1 || config.Expand(layout.RepoPaths[0]) != source {
		t.Fatalf("RepoPaths = %v, want %s", layout.RepoPaths, source)
	}
}

func TestDetectRanksByRepoCount(t *testing.T) {
	fakeHome(t,
		"Documents/Program/a", "Documents/Program/b", "Documents/Program/Quant/c",
		"code/one",
	)
	l := config.DetectLayout()

	if len(l.ScanRoots) == 0 {
		t.Fatal("nothing detected")
	}
	if l.ScanRoots[0] != "~/Documents/Program" {
		t.Errorf("the root with the most repos should rank first, got %v", l.ScanRoots)
	}
	if l.Found["~/Documents/Program"] != 3 {
		t.Errorf("should count depth-1 and depth-2 repos, got %d", l.Found["~/Documents/Program"])
	}
	if l.ProjectRoot != "~/Documents/Program" {
		t.Errorf("ProjectRoot = %q", l.ProjectRoot)
	}
}

func TestDetectIgnoresEmptyAndMissingRoots(t *testing.T) {
	home := fakeHome(t, "code/one")
	os.MkdirAll(filepath.Join(home, "projects"), 0o755) // exists but holds no repos

	l := config.DetectLayout()
	for _, r := range l.ScanRoots {
		if r == "~/projects" {
			t.Error("a root with no repositories should not become a scan root")
		}
		if r == "~/work" {
			t.Error("a root that does not exist should not become a scan root")
		}
	}
}

// Scanning both ~/src and ~/src/tries would report every experiment twice.
func TestDetectDropsNestedRoots(t *testing.T) {
	fakeHome(t, "src/one", "src/tries/2026-01-01-experiment")
	l := config.DetectLayout()

	if !contains(l.ScanRoots, "~/src") {
		t.Fatalf("~/src should be a scan root, got %v", l.ScanRoots)
	}
	if contains(l.ScanRoots, "~/src/tries") {
		t.Errorf("~/src/tries is covered by ~/src and should be dropped, got %v", l.ScanRoots)
	}
	// It is still the tries root — that is a different role from being scanned.
	if l.TriesRoot != "~/src/tries" {
		t.Errorf("TriesRoot = %q", l.TriesRoot)
	}
}

func TestDetectHonoursTryPath(t *testing.T) {
	home := fakeHome(t, "code/one")
	custom := filepath.Join(home, "my-experiments")
	os.MkdirAll(custom, 0o755)
	t.Setenv("TRY_PATH", custom)

	l := config.DetectLayout()
	if config.Expand(l.TriesRoot) != custom {
		t.Errorf("TRY_PATH should win, got %q", l.TriesRoot)
	}
}

func TestDetectHonoursGhqRoot(t *testing.T) {
	home := fakeHome(t)
	ghq := filepath.Join(home, "custom-ghq")
	os.MkdirAll(filepath.Join(ghq, "github.com", "owner", "repo", ".git"), 0o755)
	t.Setenv("GHQ_ROOT", ghq)

	l := config.DetectLayout()
	found := false
	for _, r := range l.ScanRoots {
		if config.Expand(r) == ghq {
			found = true
		}
	}
	if !found {
		t.Errorf("GHQ_ROOT should be detected, got %v", l.ScanRoots)
	}
}

// A machine with no recognisable layout must still get a complete, usable
// config rather than one with empty paths.
func TestFallbacksFillEverything(t *testing.T) {
	fakeHome(t)
	l := config.DetectLayout().Fallbacks()

	if len(l.ScanRoots) == 0 {
		t.Error("ScanRoots should fall back to the defaults")
	}
	if l.ProjectRoot == "" || l.TriesRoot == "" || l.WorktreeRoot == "" {
		t.Errorf("every path should be filled: %+v", l)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
