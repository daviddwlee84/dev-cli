package dotfile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func isolatedHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	t.Setenv("XDG_CONFIG_DIRS", filepath.Join(h, "other-config"))
	t.Setenv("XDG_DATA_DIRS", filepath.Join(h, "other-data"))
	return h
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestObserveNativeConfigFormats(t *testing.T) {
	for _, extension := range []string{"toml", "yaml", "json", "jsonc"} {
		t.Run(extension, func(t *testing.T) {
			h := isolatedHome(t)
			source := filepath.Join(h, "custom source")
			if err := os.MkdirAll(filepath.Join(source, "home"), 0o700); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(source)
			body := "sourceDir = " + string(encoded) + "\n"
			switch extension {
			case "yaml":
				body = "sourceDir: " + string(encoded) + "\n"
			case "json":
				body = `{"sourceDir":` + string(encoded) + `,"data":{"secret":"never-export"}}`
			case "jsonc":
				body = "// comment\n{\"sourceDir\":" + string(encoded) + ",}"
			}
			write(t, filepath.Join(h, "config", "chezmoi", "chezmoi."+extension), body)
			write(t, filepath.Join(source, ".chezmoiroot"), "home\n")
			s := Observe(context.Background(), Options{})
			if s.ConfigState != "present" || s.SourceState != "present" || s.SourceDir != source || s.SourceStateDir != filepath.Join(source, "home") || s.DeploymentDrift != "unknown" {
				t.Fatalf("status = %+v", s)
			}
			encodedStatus, _ := json.Marshal(s)
			if strings.Contains(string(encodedStatus), "never-export") {
				t.Fatal("config data leaked")
			}
		})
	}
}

func TestObserveDoesNotFallbackFromInvalidOrMissingConfiguredSource(t *testing.T) {
	for _, test := range []struct{ name, body, configState, sourceState, reason string }{
		{"invalid", "sourceDir = [ secret-value", "unknown", "unknown", "config-invalid"},
		{"expression", "sourceDir = '$MY_SOURCE'", "present", "unknown", "source-unresolved"},
		{"missing", "sourceDir = '~/missing-source'", "present", "absent", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := isolatedHome(t)
			if err := os.MkdirAll(filepath.Join(h, "data", "chezmoi"), 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(h, "config", "chezmoi", "chezmoi.toml"), test.body)
			s := Observe(context.Background(), Options{})
			if s.ConfigState != test.configState || s.SourceState != test.sourceState || s.Reason != test.reason {
				t.Fatalf("status = %+v", s)
			}
		})
	}
}

func TestObserveAmbiguousConfigAndExplicitOverride(t *testing.T) {
	h := isolatedHome(t)
	tomlPath := filepath.Join(h, "config", "chezmoi", "chezmoi.toml")
	write(t, tomlPath, "")
	write(t, filepath.Join(h, "config", "chezmoi", "chezmoi.json"), "{}")
	if got := Observe(context.Background(), Options{}); got.Reason != "config-ambiguous" || got.SourceState != "unknown" {
		t.Fatalf("ambiguous = %+v", got)
	}
	if got := Observe(context.Background(), Options{ConfigPath: tomlPath}); got.ConfigState != "present" || got.SourceState != "absent" {
		t.Fatalf("explicit = %+v", got)
	}
}

func TestObserveAbsentChezmoiStillReportsAbsentSource(t *testing.T) {
	h := isolatedHome(t)
	t.Setenv("PATH", h)
	s := Observe(context.Background(), Options{})
	if s.Installed || s.ConfigState != "absent" || s.SourceState != "absent" || s.Reason != "" {
		t.Fatalf("status = %+v", s)
	}
}

func TestObserveSearchesNativeXDGConfigAndDataDirectories(t *testing.T) {
	for _, mode := range []string{"secondary-config", "secondary-source"} {
		t.Run(mode, func(t *testing.T) {
			h := isolatedHome(t)
			secondaryConfig := filepath.Join(h, "shared-config")
			secondaryData := filepath.Join(h, "shared-data")
			t.Setenv("XDG_CONFIG_DIRS", secondaryConfig)
			t.Setenv("XDG_DATA_DIRS", secondaryData)
			source := filepath.Join(secondaryData, "chezmoi")
			write(t, filepath.Join(source, "dot_configured"), "keep")
			if mode == "secondary-config" {
				encoded, _ := json.Marshal(source)
				write(t, filepath.Join(secondaryConfig, "chezmoi", "chezmoi.toml"), "sourceDir = "+string(encoded)+"\n[data]\nsecret='never-print'\n")
			}
			status := Observe(t.Context(), Options{})
			if status.SourceState != "present" || status.SourceDir != source {
				t.Fatalf("secondary native source was missed: %+v", status)
			}
			if mode == "secondary-config" && (status.ConfigState != "present" || status.ConfigPath != filepath.Join(secondaryConfig, "chezmoi", "chezmoi.toml")) {
				t.Fatalf("secondary native configuration was missed: %+v", status)
			}
			plan, err := PlanSetup(t.Context(), Options{}, SetupRequest{RepoURL: "https://example.test/replacement.git", Apply: true})
			if err != nil || plan.Mode != "existing-source" || plan.ready {
				t.Fatalf("native existing source authorized initialization: %+v %v", plan, err)
			}
		})
	}
}

func TestObserveNativeXDGPrecedenceAndPerDirectoryAmbiguity(t *testing.T) {
	h := isolatedHome(t)
	first, second := filepath.Join(h, "first"), filepath.Join(h, "second")
	t.Setenv("XDG_CONFIG_DIRS", strings.Join([]string{first, second, first}, string(os.PathListSeparator)))
	firstConfig := filepath.Join(first, "chezmoi", "chezmoi.yaml")
	write(t, firstConfig, "{}")
	write(t, filepath.Join(second, "chezmoi", "chezmoi.toml"), "invalid = [")
	write(t, filepath.Join(second, "chezmoi", "chezmoi.json"), "{}")
	if status := Observe(t.Context(), Options{}); status.ConfigPath != firstConfig || status.ConfigState != "present" {
		t.Fatalf("later directory incorrectly displaced the first native config: %+v", status)
	}
	write(t, filepath.Join(first, "chezmoi", "chezmoi.toml"), "")
	if status := Observe(t.Context(), Options{}); status.Reason != "config-ambiguous" {
		t.Fatalf("multiple formats in selected directory were accepted: %+v", status)
	}
	primary := filepath.Join(h, "config", "chezmoi", "chezmoi.toml")
	write(t, primary, "")
	if status := Observe(t.Context(), Options{}); status.ConfigState != "present" || status.ConfigPath != primary {
		t.Fatalf("config home did not win over secondary directories: %+v", status)
	}
	if status := Observe(t.Context(), Options{ConfigPath: firstConfig}); status.ConfigState != "present" || status.ConfigPath != firstConfig {
		t.Fatalf("explicit config did not override default search ambiguity: %+v", status)
	}
}

func TestObserveSourceSearchOrderAndRelativeXDGHome(t *testing.T) {
	h := isolatedHome(t)
	t.Chdir(h)
	t.Setenv("XDG_DATA_HOME", "relative-data")
	first, second := filepath.Join(h, "first"), filepath.Join(h, "second")
	t.Setenv("XDG_DATA_DIRS", strings.Join([]string{first, second}, string(os.PathListSeparator)))
	write(t, filepath.Join(second, "chezmoi", "keep"), "second")
	if status := Observe(t.Context(), Options{}); status.SourceDir != filepath.Join(second, "chezmoi") || status.SourceState != "present" {
		t.Fatalf("source search did not select existing lower-priority directory: %+v", status)
	}
	write(t, filepath.Join(first, "chezmoi", "keep"), "first")
	if status := Observe(t.Context(), Options{}); status.SourceDir != filepath.Join(first, "chezmoi") {
		t.Fatalf("source search order differs from native: %+v", status)
	}
	write(t, filepath.Join(h, "relative-data", "chezmoi", "keep"), "home")
	if status := Observe(t.Context(), Options{}); status.SourceDir != filepath.Join(h, "relative-data", "chezmoi") {
		t.Fatalf("relative native XDG home incorrectly fell back to HOME: %+v", status)
	}
}

func TestObserveDanglingNativePathsAreNotBlankSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires native symlink permissions")
	}
	for _, mode := range []string{"config", "source"} {
		t.Run(mode, func(t *testing.T) {
			h := isolatedHome(t)
			path := filepath.Join(h, "data", "chezmoi")
			if mode == "config" {
				path = filepath.Join(h, "config", "chezmoi", "chezmoi.toml")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(h, "missing"), path); err != nil {
				t.Fatal(err)
			}
			status := Observe(t.Context(), Options{})
			if status.SourceState != "unknown" || status.Reason != mode+"-unreadable" {
				t.Fatalf("dangling native path was treated as uninitialized: %+v", status)
			}
			plan, err := PlanSetup(t.Context(), Options{}, SetupRequest{RepoURL: "https://example.test/replacement.git"})
			if err == nil || plan.ready {
				t.Fatalf("dangling native path authorized setup: %+v %v", plan, err)
			}
		})
	}
}

func TestObserveChezmoiExportedVariablesDoNotOverrideNativePaths(t *testing.T) {
	h := isolatedHome(t)
	t.Setenv("CHEZMOI_SOURCE_DIR", filepath.Join(h, "exported-source"))
	t.Setenv("CHEZMOI_CONFIG_FILE", filepath.Join(h, "exported-config.toml"))
	if status := Observe(t.Context(), Options{}); status.ConfigState != "absent" || status.SourceDir != filepath.Join(h, "data", "chezmoi") || status.SourceState != "absent" {
		t.Fatalf("native output-only environment changed authority: %+v", status)
	}
}

func TestObserveDoesNotTreatConventionalYMLAsNativeConfiguration(t *testing.T) {
	h := isolatedHome(t)
	path := filepath.Join(h, "config", "chezmoi", "chezmoi.yml")
	write(t, path, "sourceDir: /unrecognized\n")
	if s := Observe(context.Background(), Options{}); s.ConfigState != "absent" || s.SourceState != "absent" {
		t.Fatalf("unrecognized default config = %+v", s)
	}
	if s := Observe(context.Background(), Options{ConfigPath: path}); s.ConfigState != "unknown" || s.SourceState != "unknown" {
		t.Fatalf("unsupported explicit format = %+v", s)
	}
}

func TestObserveRejectsEscapingOrTemplatedSourceRoot(t *testing.T) {
	for _, root := range []string{"../other", "{{ output \"command\" }}", "/tmp", "home\nother"} {
		t.Run(root, func(t *testing.T) {
			h := isolatedHome(t)
			write(t, filepath.Join(h, "data", "chezmoi", ".chezmoiroot"), root)
			if got := Observe(context.Background(), Options{}); got.SourceState != "unknown" || got.Reason != "source-root-unresolved" {
				t.Fatalf("status = %+v", got)
			}
		})
	}
}

func TestObserveRevisionWithoutChezmoiOrGitHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable hook fixtures")
	}
	h := isolatedHome(t)
	source := filepath.Join(h, "data", "chezmoi")
	write(t, filepath.Join(source, ".chezmoiroot"), "home\n")
	write(t, filepath.Join(source, "home", "dot_test"), "managed")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", source}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.test")
	git("config", "core.hooksPath", filepath.Join(h, "empty-hooks"))
	git("add", ".")
	git("commit", "-m", "initial")
	head := git("rev-parse", "HEAD")
	sentinel := filepath.Join(h, "executed")
	program := "#!/bin/sh\nprintf called >> '" + sentinel + "'\nexit 99\n"
	bin := filepath.Join(h, "bin", "chezmoi")
	write(t, bin, program)
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	git("config", "core.fsmonitor", bin)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_DIR", filepath.Join(h, "wrong-repository"))
	write(t, filepath.Join(h, "config", "chezmoi", "chezmoi.toml"), "[hooks.read-source-state.pre]\ncommand = 'chezmoi'\n")
	s := Observe(context.Background(), Options{})
	if !s.Installed || s.GitRevision != head || s.GitBranch != "main" || filepath.Base(s.WorkingTree) != "chezmoi" {
		t.Fatalf("status = %+v", s)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("passive observation executed a native hook")
	}
	if _, err := os.Stat(filepath.Join(h, "cache")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("passive observation created cache")
	}
}

func TestPlatformRecommendations(t *testing.T) {
	for _, test := range []struct {
		goos, prefix, termux, release, kernel, want, repo string
		ish, openwrt                                      bool
	}{
		{goos: "darwin", want: "darwin", repo: "dotfiles"},
		{goos: "windows", want: "windows", repo: "dotfiles-windows"},
		{goos: "linux", want: "linux", repo: "dotfiles"},
		{goos: "linux", kernel: "5.15-microsoft-standard-WSL2", want: "wsl", repo: "dotfiles"},
		{goos: "linux", prefix: "/data/data/com.termux/files/usr", want: "termux", repo: "dotfiles-Termux"},
		{goos: "linux", release: "ID=openwrt\n", want: "openwrt", repo: "dotfiles-OpenWrt"},
		{goos: "linux", ish: true, want: "ish", repo: "dotfiles-iSH"},
		{goos: "linux", termux: "1", openwrt: true, want: "openwrt", repo: "dotfiles-OpenWrt"},
		{goos: "linux", termux: "1", ish: true, want: "ish", repo: "dotfiles-iSH"},
		{goos: "android", want: "android"},
		{goos: "linux", termux: "1", release: "ID=ubuntu", want: "proot"},
		{goos: "freebsd", want: "freebsd"},
	} {
		t.Run(test.want, func(t *testing.T) {
			platform := platformFrom(test.goos, test.prefix, test.termux, test.release, test.kernel, test.ish, test.openwrt)
			if platform != test.want {
				t.Fatalf("platform = %s", platform)
			}
			rec := Recommended(platform)
			if test.repo == "" {
				if rec != nil {
					t.Fatal("unsupported platform got a preset")
				}
			} else if rec == nil || rec.RepoURL != "https://github.com/daviddwlee84/"+test.repo+".git" {
				t.Fatalf("recommendation = %+v", rec)
			}
		})
	}
}
