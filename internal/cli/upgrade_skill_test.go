package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/skill"
)

func TestUpgradedExecutableUsesStableManagerPaths(t *testing.T) {
	for _, tc := range []struct {
		method    installMethod
		old, want string
	}{
		{methodHomebrew, "/opt/homebrew/Cellar/dev-cli/0.2.22/bin/dev", "/opt/homebrew/opt/dev-cli/bin/dev"},
		{methodHomebrew, "/home/linuxbrew/.linuxbrew/Cellar/dev-cli/0.2.22/bin/dev", "/home/linuxbrew/.linuxbrew/opt/dev-cli/bin/dev"},
		{methodScoop, "C:/Users/u/scoop/apps/dev-cli/0.2.22/dev.exe", "C:/Users/u/scoop/apps/dev-cli/current/dev.exe"},
	} {
		got, err := upgradedExecutable(detectedInstall{Resolved: filepath.FromSlash(tc.old), Method: tc.method})
		if err != nil || got != filepath.FromSlash(tc.want) {
			t.Fatalf("upgradedExecutable(%s) = %s, %v", tc.old, got, err)
		}
	}
}

func TestRefreshSkillAfterUpgradeUsesNewExecutableAndSkipsAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execution fixture uses a POSIX shell; path selection is tested natively")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out, errOut bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &errOut}
	// No installation means no attempt to resolve or launch another executable.
	if err := refreshSkillAfterUpgrade(context.Background(), app, detectedInstall{Resolved: "/missing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Install(skill.DefaultDir(), false); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(home, "brew", "Cellar", "dev-cli", "0.2.22", "bin", "dev")
	updated := filepath.Join(home, "brew", "opt", "dev-cli", "bin", "dev")
	if err := os.MkdirAll(filepath.Dir(updated), 0o755); err != nil {
		t.Fatal(err)
	}
	// A changed payload proves that the new executable ran, instead of copying
	// the old process's embedded data. PATH deliberately has no dev executable.
	fixture := "#!/bin/sh\n[ \"$*\" = 'skill install --if-installed' ] || exit 7\nprintf 'new binary payload' > \"$HOME/.agents/skills/dev-cli/SKILL.md\"\n"
	if err := os.WriteFile(updated, []byte(fixture), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	install := detectedInstall{Path: old, Resolved: old, Method: methodHomebrew}
	if err := refreshSkillAfterUpgrade(context.Background(), app, install); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(skill.DefaultDir(), "SKILL.md"))
	if string(got) != "new binary payload" {
		t.Fatalf("installed payload = %q", got)
	}
	if err := os.WriteFile(updated, []byte("#!/bin/sh\nexit 8\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := refreshSkillAfterUpgrade(context.Background(), app, install); err == nil || !strings.Contains(err.Error(), "binary update completed") {
		t.Fatalf("refresh failure did not report partial result: %v", err)
	}
}

func TestBundledSkillDoctorCheckIsReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got := bundledSkillDoctorCheck(); got.status != checkOK || !strings.Contains(got.detail, "not installed") {
		t.Fatalf("absent = %+v", got)
	}
	if _, err := skill.Install(skill.DefaultDir(), false); err != nil {
		t.Fatal(err)
	}
	if got := bundledSkillDoctorCheck(); got.status != checkOK || !strings.Contains(got.detail, "matches") {
		t.Fatalf("current = %+v", got)
	}
	path := filepath.Join(skill.DefaultDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("personal edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := bundledSkillDoctorCheck(); got.status != checkWarn || !strings.Contains(got.detail, "local edits") {
		t.Fatalf("modified = %+v", got)
	}
	if body, _ := os.ReadFile(path); string(body) != "personal edit" {
		t.Fatal("doctor changed installed content")
	}
}

func TestBundledSkillUninstallRequiresConfirmation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), skill.Name)
	if _, err := skill.Install(dir, false); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := &App{In: strings.NewReader("n\n"), Out: &out, Err: &out}
	cmd := newSkillUninstallCmd(app)
	cmd.SetArgs([]string{"--dir", dir})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("declined uninstall = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal("declined uninstall removed files")
	}
}
