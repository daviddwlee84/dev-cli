package repo

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

func discoveryFixture(t *testing.T) (string, string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	root := filepath.Join(base, "projects", "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	r, err := gitx.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "custom.toml")
	return root, r.GitCommonDir, file
}

func TestDiscoveryRegistrationPreviewApplyAndRecovery(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("configuration write backend")
	}
	for _, scope := range []DiscoveryScope{DiscoveryExact, DiscoveryParent} {
		t.Run(string(scope), func(t *testing.T) {
			root, common, file := discoveryFixture(t)
			raw := []byte("# keep me\n[paths]\nscan_roots = []\nrepo_paths = [] # exact entries\n[runtime]\nbackend = 'none'\n")
			if err := os.WriteFile(file, raw, 0o640); err != nil {
				t.Fatal(err)
			}
			p, err := PlanDiscoveryRegistration(t.Context(), file, root, common, scope)
			if err != nil || p.Blocked != "" {
				t.Fatalf("plan: %+v %v", p, err)
			}
			before, _ := os.ReadFile(file)
			if !bytes.Equal(before, raw) {
				t.Fatal("preview wrote config")
			}
			wantPath := root
			if scope == DiscoveryParent {
				wantPath = filepath.Dir(root)
			}
			if p.File != file || p.Path != wantPath || !strings.Contains(p.After, filepath.ToSlash(wantPath)) {
				t.Fatal(p)
			}
			result, err := ApplyDiscoveryRegistration(t.Context(), p)
			if err != nil || result.Status != "applied" {
				t.Fatal(result, err)
			}
			cfg, err := config.Load(file)
			if err != nil {
				t.Fatal(err)
			}
			if covered, err := DiscoveryCovered(cfg, root); err != nil || !covered {
				t.Fatal("not covered", err)
			}
			info, _ := os.Stat(file)
			if info.Mode().Perm() != 0o640 {
				t.Fatal("lost permissions")
			}
			if _, err := PlanDiscoveryRegistration(t.Context(), file, root, common, scope); err == nil {
				t.Fatal("allowed duplicate registration")
			}
			recovery := filepath.Join(config.DataHome(), "dev", "config-recovery")
			restore, err := configedit.RestorePlan(t.Context(), recovery, result.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := configedit.Apply(t.Context(), restore, recovery); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(after, raw) {
				t.Fatal("recovery lost original bytes")
			}
		})
	}
}

func TestDiscoveryRegistrationRejectsChangedConfigAndIdentity(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("configuration write backend")
	}
	root, common, file := discoveryFixture(t)
	raw := []byte("[paths]\nscan_roots = []\n")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := PlanDiscoveryRegistration(t.Context(), file, root, common, DiscoveryExact)
	if err != nil {
		t.Fatal(err)
	}
	changed := append(bytes.Clone(raw), []byte("# concurrent edit\n")...)
	if err := os.WriteFile(file, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyDiscoveryRegistration(t.Context(), p); !errors.Is(err, configedit.ErrStale) {
		t.Fatal("stale plan accepted", err)
	}
	if data, _ := os.ReadFile(file); !bytes.Equal(data, changed) {
		t.Fatal("overwrote concurrent edit")
	}
	p, err = PlanDiscoveryRegistration(t.Context(), file, root, common, DiscoveryExact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, "moved-git")); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("recreate Git: %v %s", err, output)
	}
	if _, err := ApplyDiscoveryRegistration(t.Context(), p); err == nil {
		t.Fatal("changed Git identity accepted")
	}
	if data, _ := os.ReadFile(file); !bytes.Equal(data, changed) {
		t.Fatal("wrote after Git identity change")
	}
}

func TestDiscoveryRegistrationMissingConfigAndBlockedEdit(t *testing.T) {
	root, common, file := discoveryFixture(t)
	p, err := PlanDiscoveryRegistration(t.Context(), file, root, common, DiscoveryExact)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if _, err := ApplyDiscoveryRegistration(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(file)
		if err != nil || len(cfg.Paths.ScanRoots) != len(config.Default().Paths.ScanRoots) {
			t.Fatal("lost defaults", err)
		}
	}
	if err := os.WriteFile(file, []byte("paths = { scan_roots = [], repo_paths = [] }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err = PlanDiscoveryRegistration(t.Context(), file, root, common, DiscoveryExact)
	if err != nil || p.Blocked == "" {
		t.Fatal("inline config should offer manual edit", err)
	}
	if _, err := ApplyDiscoveryRegistration(t.Context(), p); err == nil {
		t.Fatal("applied blocked plan")
	}
	alias := file + ".link"
	if err := os.Symlink(file, alias); err != nil {
		t.Skip(err)
	}
	p, err = PlanDiscoveryRegistration(t.Context(), alias, root, common, DiscoveryExact)
	if err != nil || p.Blocked == "" {
		t.Fatal("symlink config should offer manual edit", err)
	}
}

func TestDiscoveryCoverageHonorsAliasesAndTraversal(t *testing.T) {
	root, _, file := discoveryFixture(t)
	cfg := config.Default()
	cfg.Paths.ScanRoots = []string{filepath.Dir(root)}
	cfg.Paths.RepoPaths = nil
	if covered, err := DiscoveryCovered(cfg, root); err != nil || !covered {
		t.Fatal(covered, err)
	}
	alias := file + ".repo"
	if err := os.Symlink(root, alias); err != nil {
		t.Skip(err)
	}
	cfg.Paths.ScanRoots = nil
	cfg.Paths.RepoPaths = []string{alias}
	if covered, err := DiscoveryCovered(cfg, root); err != nil || !covered {
		t.Fatal(covered, err)
	}
	cfg.Paths.RepoPaths = nil
	cfg.Paths.ScanRoots = []string{filepath.Dir(root)}
	if covered, err := DiscoveryCovered(cfg, filepath.Join(filepath.Dir(root), ".hidden", "repo")); err != nil || covered {
		t.Fatal(covered, err)
	}
}
