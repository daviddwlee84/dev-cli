//go:build darwin || linux

package sshhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func permissionFixture(t *testing.T) (*Service, Paths) {
	t.Helper()
	home := t.TempDir()
	paths, err := NewPaths(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.SSHDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.SSHDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.RootConfig, []byte("# preserve exactly\r\nHost foreign\r\n  HostName foreign.example\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.RootConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.ManagedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.ManagedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(paths, permissionNoRunner{t})
	if err != nil {
		t.Fatal(err)
	}
	return service, paths
}

type permissionNoRunner struct{ t *testing.T }

func (r permissionNoRunner) Run(context.Context, RunRequest) (RunResult, error) {
	r.t.Fatal("permission preflight invoked a subprocess")
	return RunResult{}, nil
}

func permissionMode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestPermissionPlanAndApplyCanonicalModesWithoutChangingContents(t *testing.T) {
	service, paths := permissionFixture(t)
	before, err := os.ReadFile(paths.RootConfig)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, _ := os.Lstat(paths.RootConfig)
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{})
	if err != nil || !plan.Ready() || !plan.NeedsRepair() || plan.Status != "repairable" || len(plan.Changes) != 3 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	for path, want := range map[string]fs.FileMode{paths.SSHDir: 0o755, paths.ManagedDir: 0o755, paths.RootConfig: 0o644} {
		if permissionMode(t, path) != want {
			t.Fatal("planning changed mode")
		}
	}
	lockPath, _ := OperationLockPath(paths)
	if _, err := os.Lstat(lockPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("planning created lock")
	}
	result, err := service.ApplyPermissions(context.Background(), plan)
	if err != nil || result.Status != "applied" || len(result.Outcomes) != 3 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, outcome := range result.Outcomes {
		if outcome.Status != "applied" {
			t.Fatal(outcome)
		}
	}
	for path, want := range map[string]fs.FileMode{paths.SSHDir: 0o700, paths.ManagedDir: 0o700, paths.RootConfig: 0o600} {
		if permissionMode(t, path) != want {
			t.Fatalf("mode %s", path)
		}
	}
	after, err := os.ReadFile(paths.RootConfig)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("repair changed configuration bytes")
	}
	afterInfo, _ := os.Lstat(paths.RootConfig)
	if !os.SameFile(beforeInfo, afterInfo) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("mode repair replaced file or changed content mtime")
	}
	fresh, err := service.PlanPermissions(context.Background(), PermissionRequest{})
	if err != nil || !fresh.Ready() || fresh.NeedsRepair() {
		t.Fatalf("repeat=%#v %v", fresh, err)
	}
}

func TestPermissionSelectedPublicPathInspectsPrivateCompanionAndParents(t *testing.T) {
	service, paths := permissionFixture(t)
	directory := filepath.Join(paths.SSHDir, "keys")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(directory, "chosen")
	privateBytes := []byte("arbitrary invalid private bytes MUST NOT BE PARSED OR MARSHALLED")
	if err := os.WriteFile(identity, privateBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(identity, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identity+".pub", []byte("invalid public bytes are not parsed either"), 0o644); err != nil {
		t.Fatal(err)
	}
	unselected := filepath.Join(paths.SSHDir, "unselected")
	if err := os.WriteFile(unselected, privateBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unselected, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{KeyPath: identity + ".pub"})
	if err != nil || !plan.Ready() || len(plan.Changes) != 5 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	encoded, _ := json.Marshal(plan)
	if bytes.Contains(encoded, privateBytes) {
		t.Fatal("private bytes escaped plan")
	}
	if _, err := service.ApplyPermissions(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if permissionMode(t, identity) != 0o600 || permissionMode(t, directory) != 0o700 || permissionMode(t, identity+".pub") != 0o644 || permissionMode(t, unselected) != 0o644 {
		t.Fatal("selected-only repair scope was not retained")
	}
}

func TestPermissionPublicRepairOnlyRemovesGroupWorldWrite(t *testing.T) {
	service, paths := permissionFixture(t)
	public := filepath.Join(paths.SSHDir, "chosen.pub")
	if err := os.WriteFile(public, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0o666); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{KeyPath: public})
	if err != nil || !plan.Ready() {
		t.Fatalf("%#v %v", plan, err)
	}
	if _, err := service.ApplyPermissions(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if permissionMode(t, public) != 0o644 {
		t.Fatal("public mode was not tightened to0644")
	}
}

func TestPermissionMissingCanonicalPathsDoNotCreateAnything(t *testing.T) {
	home := t.TempDir()
	paths, _ := NewPaths(home)
	service, err := NewService(paths, permissionNoRunner{t})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{KeyPath: filepath.Join(paths.SSHDir, "new-key")})
	if err != nil || !plan.Ready() || plan.NeedsRepair() {
		t.Fatalf("%#v %v", plan, err)
	}
	if _, err := service.ApplyPermissions(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("no-op preflight wrote files: %#v %v", entries, err)
	}
}

func TestPermissionRejectsUnsafeTypesLinksAndPermissionExpansion(t *testing.T) {
	for _, kind := range []string{"ssh_symlink", "config_symlink", "key_hardlink", "key_fifo", "owner_readonly", "special_bits", "nested_missing"} {
		t.Run(kind, func(t *testing.T) {
			service, paths := permissionFixture(t)
			key := filepath.Join(paths.SSHDir, "key")
			if err := os.WriteFile(key, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			request := PermissionRequest{KeyPath: key}
			switch kind {
			case "ssh_symlink":
				moved := paths.SSHDir + "-moved"
				if err := os.Rename(paths.SSHDir, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, paths.SSHDir); err != nil {
					t.Fatal(err)
				}
			case "config_symlink":
				if err := os.Remove(paths.RootConfig); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(key, paths.RootConfig); err != nil {
					t.Fatal(err)
				}
			case "key_hardlink":
				if err := os.Link(key, key+"-link"); err != nil {
					t.Fatal(err)
				}
			case "key_fifo":
				if err := os.Remove(key); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(key, 0o600); err != nil {
					t.Fatal(err)
				}
			case "owner_readonly":
				if err := os.Chmod(key, 0o400); err != nil {
					t.Fatal(err)
				}
			case "special_bits":
				if err := os.Chmod(paths.SSHDir, 0o755|os.ModeSetgid); err != nil {
					t.Fatal(err)
				}
			case "nested_missing":
				request.KeyPath = filepath.Join(paths.SSHDir, "missing", "key")
			}
			plan, err := service.PlanPermissions(context.Background(), request)
			if err != nil || plan.Ready() || plan.Status != "blocked" || len(plan.Diagnostics) == 0 {
				t.Fatalf("%#v %v", plan, err)
			}
			if _, err := service.ApplyPermissions(context.Background(), plan); !errors.Is(err, ErrBlocked) {
				t.Fatal(err)
			}
			lockPath, _ := OperationLockPath(paths)
			if _, err := os.Lstat(lockPath); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("blocked plan created lock")
			}
		})
	}
}

func TestPermissionPlansAreServiceBoundAndImmutable(t *testing.T) {
	service, paths := permissionFixture(t)
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewService(paths, permissionNoRunner{t})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ApplyPermissions(context.Background(), plan); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	plan.Changes[0].AfterMode = 0o777
	if _, err := service.ApplyPermissions(context.Background(), plan); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	if permissionMode(t, paths.SSHDir) != 0o755 {
		t.Fatal("tampered plan changed permissions")
	}
}

func TestPermissionRevalidatesReplacementAndModeBeforeAnyChmod(t *testing.T) {
	for _, kind := range []string{"same_path_replacement", "mode_change", "parent_replacement"} {
		t.Run(kind, func(t *testing.T) {
			service, paths := permissionFixture(t)
			plan, err := service.PlanPermissions(context.Background(), PermissionRequest{})
			if err != nil {
				t.Fatal(err)
			}
			plan.state.beforeChange = func(index int) {
				if index != 0 {
					return
				}
				switch kind {
				case "same_path_replacement":
					if err := os.Rename(paths.RootConfig, paths.RootConfig+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(paths.RootConfig, []byte("replacement"), 0o644); err != nil {
						t.Fatal(err)
					}
				case "mode_change":
					if err := os.Chmod(paths.SSHDir, 0o750); err != nil {
						t.Fatal(err)
					}
				case "parent_replacement":
					if err := os.Rename(paths.SSHDir, paths.SSHDir+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(paths.SSHDir, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := service.ApplyPermissions(context.Background(), plan)
			if !errors.Is(err, ErrSourceChanged) || result.Status != "not_run" {
				t.Fatalf("%#v %v", result, err)
			}
			for _, outcome := range result.Outcomes {
				if outcome.Status != "not_run" {
					t.Fatal(outcome)
				}
			}
		})
	}
}

func TestPermissionCancellationKeepsCompletedTightening(t *testing.T) {
	service, paths := permissionFixture(t)
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plan.state.beforeChange = func(index int) {
		if index == 1 {
			cancel()
		}
	}
	result, err := service.ApplyPermissions(ctx, plan)
	if !errors.Is(err, context.Canceled) || result.Status != "partial" || result.Outcomes[0].Status != "applied" || result.Outcomes[1].Status != "not_run" {
		t.Fatalf("%#v %v", result, err)
	}
	if permissionMode(t, paths.SSHDir) != 0o700 || permissionMode(t, paths.RootConfig) != 0o644 {
		t.Fatal("completed repair was widened or unattempted repair applied")
	}
}

func TestPermissionReplacementAfterChmodIsUnknownAndNeverRestored(t *testing.T) {
	service, paths := permissionFixture(t)
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	plan.state.afterChange = func(index int) {
		if index != 0 {
			return
		}
		if err := os.Rename(paths.SSHDir, paths.SSHDir+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(paths.SSHDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.ApplyPermissions(context.Background(), plan)
	if !errors.Is(err, ErrSourceChanged) || result.Status != "partial" || result.Outcomes[0].Status != "unknown" {
		t.Fatalf("%#v %v", result, err)
	}
	if permissionMode(t, paths.SSHDir+".old") != 0o700 || permissionMode(t, paths.SSHDir) != 0o755 {
		t.Fatal("replacement path was chmodded or old permissions widened")
	}
}

func TestPermissionFinalCheckCatchesEarlierTargetChangedDuringLastRepair(t *testing.T) {
	service, paths := permissionFixture(t)
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	plan.state.afterChange = func(index int) {
		if index == len(plan.Changes)-1 {
			if err := os.Chmod(paths.SSHDir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := service.ApplyPermissions(context.Background(), plan)
	if !errors.Is(err, ErrSourceChanged) || result.Status != "partial" {
		t.Fatalf("%#v %v", result, err)
	}
	if permissionMode(t, paths.RootConfig) != 0o600 {
		t.Fatal("successful file tightening was rolled back")
	}
}
