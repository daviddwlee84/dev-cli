//go:build darwin || linux

package sshhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func permissionScanFile(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("not key material; permission scans must never parse file contents"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionScanRecognizedScopeFeedsOneDeduplicatedRepairPlan(t *testing.T) {
	service, paths := permissionFixture(t)
	nested := filepath.Join(paths.SSHDir, "keys")
	unrelated := filepath.Join(paths.SSHDir, "unrelated")
	for _, directory := range []string{nested, unrelated} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	standard := filepath.Join(paths.SSHDir, "id_ed25519")
	paired := filepath.Join(paths.SSHDir, "custom")
	child := filepath.Join(nested, "chosen")
	ignored := []string{filepath.Join(paths.SSHDir, "unknown-private"), filepath.Join(unrelated, "id_rsa"), filepath.Join(paths.ManagedDir, "ignored.pub")}
	for _, path := range append([]string{standard, paired, paired + ".pub", child, child + ".pub"}, ignored...) {
		permissionScanFile(t, path, 0o644)
	}
	if err := os.Chmod(child+".pub", 0o666); err != nil {
		t.Fatal(err)
	}
	scan, err := service.ScanPermissionKeys(context.Background())
	want := []string{standard, paired + ".pub", child + ".pub"}
	sort.Strings(want)
	if err != nil || !scan.Complete || len(scan.Diagnostics) != 0 || !reflect.DeepEqual(scan.KeyPaths, want) {
		t.Fatalf("scan=%#v err=%v", scan, err)
	}
	if permissionMode(t, paths.SSHDir) != 0o755 || permissionMode(t, child+".pub") != 0o666 {
		t.Fatal("scan changed permissions")
	}
	lockPath, _ := OperationLockPath(paths)
	if _, err := os.Lstat(lockPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("scan acquired a mutation lock")
	}
	selected := append(append([]string{}, scan.KeyPaths...), paired, paired+".pub")
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{KeyPath: paired, KeyPaths: selected})
	if err != nil || !plan.Ready() || len(plan.Changes) != 8 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	seen := map[string]bool{}
	for _, change := range plan.Changes {
		if seen[change.Path] {
			t.Fatal("duplicate repair", change.Path)
		}
		seen[change.Path] = true
	}
	encoded, err := json.Marshal(struct {
		Scan PermissionKeyScan
		Plan PermissionPlan
	}{scan, plan})
	if err != nil || bytes.Contains(encoded, []byte("not key material")) {
		t.Fatalf("private file contents escaped metadata result: %v", err)
	}
	result, err := service.ApplyPermissions(context.Background(), plan)
	if err != nil || result.Status != "applied" || len(result.Outcomes) != 8 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, path := range []string{standard, paired, child} {
		if permissionMode(t, path) != 0o600 {
			t.Fatal("selected private companion was not repaired", path)
		}
	}
	if permissionMode(t, nested) != 0o700 || permissionMode(t, unrelated) != 0o755 || permissionMode(t, child+".pub") != 0o644 || permissionMode(t, paired+".pub") != 0o644 {
		t.Fatal("directory or public-key repair scope changed")
	}
	for _, path := range ignored {
		if permissionMode(t, path) != 0o644 {
			t.Fatal("unselected file permissions changed", path)
		}
	}
}

func TestPermissionScanMissingSSHIsCompleteWithoutCreation(t *testing.T) {
	home := t.TempDir()
	paths, _ := NewPaths(home)
	service, err := NewService(paths, permissionNoRunner{t})
	if err != nil {
		t.Fatal(err)
	}
	scan, err := service.ScanPermissionKeys(context.Background())
	if err != nil || !scan.Complete || len(scan.KeyPaths) != 0 || len(scan.Diagnostics) != 0 {
		t.Fatalf("scan=%#v err=%v", scan, err)
	}
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{KeyPaths: scan.KeyPaths})
	if err != nil || !plan.Ready() || plan.NeedsRepair() {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if _, err := service.ApplyPermissions(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scan created paths: %#v %v", entries, err)
	}
}

func TestPermissionScanUnsafeScopeNeverLooksComplete(t *testing.T) {
	for _, kind := range []string{"root_symlink", "public_symlink", "public_hardlink", "standard_symlink", "standard_fifo", "public_directory", "unsafe_name"} {
		t.Run(kind, func(t *testing.T) {
			service, paths := permissionFixture(t)
			outside := t.TempDir()
			permissionScanFile(t, filepath.Join(outside, "outside.pub"), 0o666)
			switch kind {
			case "root_symlink":
				if err := os.Rename(paths.SSHDir, paths.SSHDir+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, paths.SSHDir); err != nil {
					t.Fatal(err)
				}
			case "standard_symlink":
				if err := os.Symlink(outside, filepath.Join(paths.SSHDir, "id_ed25519")); err != nil {
					t.Fatal(err)
				}
			case "public_symlink":
				if err := os.Symlink(filepath.Join(outside, "outside.pub"), filepath.Join(paths.SSHDir, "key.pub")); err != nil {
					t.Fatal(err)
				}
			case "public_hardlink":
				permissionScanFile(t, filepath.Join(paths.SSHDir, "key.pub"), 0o644)
				if err := os.Link(filepath.Join(paths.SSHDir, "key.pub"), filepath.Join(paths.SSHDir, "linked")); err != nil {
					t.Fatal(err)
				}
			case "standard_fifo":
				if err := unix.Mkfifo(filepath.Join(paths.SSHDir, "id_ed25519"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "public_directory":
				if err := os.Mkdir(filepath.Join(paths.SSHDir, "key.pub"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "unsafe_name":
				permissionScanFile(t, filepath.Join(paths.SSHDir, "bad\nname.pub"), 0o644)
			}
			scan, err := service.ScanPermissionKeys(context.Background())
			if err != nil || scan.Complete || len(scan.KeyPaths) != 0 || len(scan.Diagnostics) == 0 {
				t.Fatalf("scan=%#v err=%v", scan, err)
			}
			for _, diagnostic := range scan.Diagnostics {
				if !diagnostic.Incomplete || !diagnostic.BlocksMutation || strings.ContainsAny(diagnostic.Path+diagnostic.Message, "\r\n\x1b") {
					t.Fatal("unsafe or misleading diagnostic", diagnostic)
				}
			}
			if permissionMode(t, filepath.Join(outside, "outside.pub")) != 0o666 {
				t.Fatal("scan followed a link")
			}
		})
	}
}

func TestPermissionScanUnrelatedLinksDoNotBlockPairedKeyRepair(t *testing.T) {
	service, paths := permissionFixture(t)
	outside := t.TempDir()
	outKey := filepath.Join(outside, "outside.pub")
	permissionScanFile(t, outKey, 0o666)
	links := map[string]string{
		filepath.Join(paths.SSHDir, "external-keys"): outside,
		filepath.Join(paths.SSHDir, "agent.sock"):    filepath.Join(outside, "agent.sock"),
		filepath.Join(paths.SSHDir, "foreign.conf"):  outKey,
	}
	identities := map[string]fs.FileInfo{}
	for path, target := range links {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		identities[path], _ = os.Lstat(path)
	}
	key := filepath.Join(paths.SSHDir, "azure")
	permissionScanFile(t, key, 0o644)
	permissionScanFile(t, key+".pub", 0o644)
	scan, err := service.ScanPermissionKeys(context.Background())
	if err != nil || !scan.Complete || len(scan.Diagnostics) != 0 || !reflect.DeepEqual(scan.KeyPaths, []string{key + ".pub"}) {
		t.Fatalf("unrelated links blocked direct key scope: %#v %v", scan, err)
	}
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{KeyPaths: scan.KeyPaths})
	if err != nil || !plan.Ready() {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if _, err := service.ApplyPermissions(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if permissionMode(t, key) != 0o600 || permissionMode(t, outKey) != 0o666 {
		t.Fatal("unexpected repair scope")
	}
	for path, target := range links {
		info, err := os.Lstat(path)
		if err != nil || !os.SameFile(identities[path], info) {
			t.Fatalf("unrelated link changed: %s %v", path, err)
		}
		if current, err := os.Readlink(path); err != nil || current != target {
			t.Fatalf("link target changed: %s %v", current, err)
		}
	}
}

func TestPermissionExplicitSelectionCanExcludeUnrelatedUnsafeCandidate(t *testing.T) {
	service, paths := permissionFixture(t)
	key := filepath.Join(paths.SSHDir, "azure")
	permissionScanFile(t, key, 0o644)
	permissionScanFile(t, key+".pub", 0o644)
	linked := filepath.Join(paths.SSHDir, "unsafe.pub")
	if err := os.Symlink(key+".pub", linked); err != nil {
		t.Fatal(err)
	}
	scan, err := service.ScanPermissionKeys(context.Background())
	if err != nil || scan.Complete {
		t.Fatalf("scan=%#v err=%v", scan, err)
	}
	plan, err := service.PlanPermissions(context.Background(), PermissionRequest{KeyPaths: []string{key + ".pub"}})
	if err != nil || !plan.Ready() {
		t.Fatalf("selected plan=%#v err=%v", plan, err)
	}
	if _, err := service.ApplyPermissions(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if permissionMode(t, key) != 0o600 {
		t.Fatal("explicit key was not repaired")
	}
	if info, err := os.Lstat(linked); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unselected link changed: %v", err)
	}
}

func TestPermissionScanBoundsRetainIncompleteState(t *testing.T) {
	for _, kind := range []string{"keys", "entries", "depth"} {
		t.Run(kind, func(t *testing.T) {
			service, paths := permissionFixture(t)
			count := maxCatalogFiles + 1
			suffix := ".pub"
			if kind == "entries" {
				count, suffix = maxCatalogFiles*8+1, ""
			}
			if kind == "depth" {
				path := paths.SSHDir
				for range maxCatalogDepth + 1 {
					path = filepath.Join(path, "nested")
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				permissionScanFile(t, filepath.Join(path, "deep.pub"), 0o644)
			} else {
				for i := range count {
					if err := os.WriteFile(filepath.Join(paths.SSHDir, fmt.Sprintf("key-%04d%s", i, suffix)), nil, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			scan, err := service.ScanPermissionKeys(context.Background())
			if err != nil || scan.Complete || len(scan.KeyPaths) > maxCatalogFiles || len(scan.Diagnostics) == 0 {
				t.Fatalf("scan=%#v err=%v", scan, err)
			}
			want := map[string]string{"keys": "permission_scan_key_limit", "entries": "permission_scan_entry_limit", "depth": "permission_scan_depth_limit"}[kind]
			found := false
			for _, diagnostic := range scan.Diagnostics {
				found = found || diagnostic.Code == want
			}
			if !found {
				t.Fatal("missing bound diagnostic", scan.Diagnostics)
			}
		})
	}
}

func TestPermissionScanDetectsSourcesChangedAfterEnumeration(t *testing.T) {
	for _, kind := range []string{"root_replaced", "directory_replaced", "key_added", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			service, paths := permissionFixture(t)
			nested := filepath.Join(paths.SSHDir, "keys")
			if err := os.Mkdir(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			permissionScanFile(t, filepath.Join(nested, "chosen.pub"), 0o644)
			permissionScanFile(t, filepath.Join(paths.SSHDir, "a.pub"), 0o644)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := false
			scan, err := service.scanPermissionKeys(ctx, func(path string) {
				if done {
					return
				}
				if (kind == "directory_replaced" || kind == "canceled") && path != nested {
					return
				}
				done = true
				switch kind {
				case "root_replaced", "directory_replaced":
					if err := os.Rename(path, path+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
				case "key_added":
					permissionScanFile(t, filepath.Join(path, "new.pub"), 0o666)
				case "canceled":
					cancel()
				}
			})
			wantErr := ErrSourceChanged
			if kind == "canceled" {
				wantErr = context.Canceled
			}
			if !errors.Is(err, wantErr) || scan.Complete {
				t.Fatalf("scan=%#v err=%v", scan, err)
			}
			if kind == "canceled" && !reflect.DeepEqual(scan.KeyPaths, []string{filepath.Join(paths.SSHDir, "a.pub")}) {
				t.Fatal("cancellation discarded prior observations", scan.KeyPaths)
			}
		})
	}
}

func TestPermissionBatchRefusesUnsafeOrReplacedOtherSelectionBeforeAnyRepair(t *testing.T) {
	for _, kind := range []string{"unsafe_selection", "replaced_selection"} {
		t.Run(kind, func(t *testing.T) {
			service, paths := permissionFixture(t)
			first, second := filepath.Join(paths.SSHDir, "first"), filepath.Join(paths.SSHDir, "second")
			permissionScanFile(t, first, 0o644)
			permissionScanFile(t, second, 0o644)
			if kind == "unsafe_selection" {
				if err := os.Remove(second); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(first, second); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := service.PlanPermissions(context.Background(), PermissionRequest{KeyPath: first, KeyPaths: []string{second, first}})
			if err != nil {
				t.Fatal(err)
			}
			wantErr := ErrBlocked
			if kind == "replaced_selection" {
				wantErr = ErrSourceChanged
				if err := os.Rename(second, second+".old"); err != nil {
					t.Fatal(err)
				}
				permissionScanFile(t, second, 0o644)
			}
			result, err := service.ApplyPermissions(context.Background(), plan)
			if !errors.Is(err, wantErr) || result.Status != "not_run" {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if permissionMode(t, paths.SSHDir) != 0o755 || permissionMode(t, first) != 0o644 {
				t.Fatal("batch partially repaired despite stale or blocked selection")
			}
		})
	}
}

func TestPermissionRequestLimitCountsDistinctResolvedPaths(t *testing.T) {
	service, paths := permissionFixture(t)
	key := filepath.Join(paths.SSHDir, "chosen")
	selected := make([]string, maxCatalogFiles+1)
	for index := range selected {
		selected[index] = key
	}
	if plan, err := service.PlanPermissions(context.Background(), PermissionRequest{KeyPath: key, KeyPaths: selected}); err != nil || !plan.Ready() {
		t.Fatalf("duplicate key selections were not unioned: %#v %v", plan, err)
	}
	for index := range selected {
		selected[index] = filepath.Join(paths.SSHDir, fmt.Sprintf("key-%d", index))
	}
	if _, err := service.PlanPermissions(context.Background(), PermissionRequest{KeyPaths: selected}); err == nil {
		t.Fatal("unbounded key selection accepted")
	}
}
