package sshhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// PermissionKeyScan is a bounded metadata-only inventory for an explicit key
// doctor. Unreadable, unsafe, changed or truncated scope is never a clean empty
// result. Callers must not fix an incomplete scan; explicit --key selections can
// instead review only those paths and the canonical setup paths.
type PermissionKeyScan struct {
	KeyPaths    []string     `json:"key_paths"`
	Complete    bool         `json:"complete"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

var permissionDefaultKeyNames = map[string]bool{
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ecdsa_sk": true,
	"id_ed25519": true, "id_ed25519_sk": true, "id_ed25519_dev": true,
}

// ScanPermissionKeys considers regular .pub files in direct owned directories
// and exact conventional identity names immediately under ~/.ssh. Unrelated
// symlinks are outside that scope; linked candidates require manual review.
// It never reads file contents,
// follows links, consults an agent, creates paths, or takes a mutation lock.
// Unknown extensionless files require an explicit key path. Traversed folders
// are never repair targets merely because the scanner passed through them.
func (s *Service) ScanPermissionKeys(ctx context.Context) (PermissionKeyScan, error) {
	return s.scanPermissionKeys(ctx, nil)
}

// afterRead is a package-test seam for source changes immediately after a held
// directory has been enumerated. Production scans always leave it nil.
func (s *Service) scanPermissionKeys(ctx context.Context, afterRead func(string)) (PermissionKeyScan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := PermissionKeyScan{KeyPaths: []string{}, Complete: true}
	if err := ctx.Err(); err != nil {
		report.Complete = false
		return report, err
	}
	diagnostic := func(code, path string, cause error) {
		report.Complete = false
		message := "key permission scan requires manual review"
		if cause != nil {
			message = cause.Error()
		}
		report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: code, Path: path, Message: message, Incomplete: true, BlocksMutation: true})
	}
	initial, initialFile, err := s.observePermissionTarget(permissionTarget{path: s.paths.SSHDir, kind: "ssh_directory", directory: true, optional: true})
	if initialFile != nil {
		_ = initialFile.Close()
	}
	if err != nil {
		diagnostic("permission_scan_root_unsafe", s.paths.SSHDir, err)
		return report, nil
	}
	if initial.info == nil {
		current, file, err := s.observePermissionTarget(initial.target)
		if file != nil {
			_ = file.Close()
		}
		if err != nil || !samePermissionSnapshot(initial, current) {
			diagnostic("permission_scan_source_changed", s.paths.SSHDir, ErrSourceChanged)
			return report, ErrSourceChanged
		}
		if err := ctx.Err(); err != nil {
			report.Complete = false
			return report, err
		}
		return report, nil
	}
	root, held, err := safefile.OpenRoot(s.paths.SSHDir)
	if err != nil {
		diagnostic("permission_scan_root_unsafe", s.paths.SSHDir, err)
		return report, nil
	}
	defer root.Close()
	if !sameSelectedKeyFileInfo(initial.info, held) {
		diagnostic("permission_scan_source_changed", s.paths.SSHDir, ErrSourceChanged)
		return report, ErrSourceChanged
	}
	selected := map[string]bool{}
	visited := 0
	stopped := false
	var observed []permissionSnapshot
	var walk func(*os.Root, string, int) error
	walk = func(directory *os.Root, path string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if stopped {
			return nil
		}
		before, file, err := s.observePermissionTarget(permissionTarget{path: path, kind: "key_directory", directory: true})
		if err != nil {
			diagnostic("permission_scan_directory_unsafe", path, err)
			return nil
		}
		defer file.Close()
		current, err := directory.Stat(".")
		if err != nil || !sameSelectedKeyFileInfo(before.info, current) {
			diagnostic("permission_scan_source_changed", path, ErrSourceChanged)
			return ErrSourceChanged
		}
		observed = append(observed, before)
		entries, readErr := file.ReadDir(maxCatalogFiles*8 - visited + 1)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			diagnostic("permission_scan_directory_unreadable", path, readErr)
			return nil
		}
		if afterRead != nil {
			afterRead(path)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			visited++
			if visited > maxCatalogFiles*8 {
				diagnostic("permission_scan_entry_limit", path, fmt.Errorf("key permission scan exceeds %d entries", maxCatalogFiles*8))
				stopped = true
				return nil
			}
			name := entry.Name()
			childPath := filepath.Join(path, name)
			if !validUTF8NoControl(name) {
				diagnostic("permission_scan_name_unsafe", fmt.Sprintf("%q", childPath), fmt.Errorf("path contains unsupported characters: %w", ErrUnsafePath))
				continue
			}
			if childPath == s.paths.ManagedDir {
				continue
			}
			candidate := strings.HasSuffix(strings.ToLower(name), ".pub") || path == s.paths.SSHDir && permissionDefaultKeyNames[name]
			info, err := directory.Lstat(name)
			if err != nil {
				diagnostic("permission_scan_entry_unreadable", childPath, err)
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				if candidate {
					diagnostic("permission_scan_link_skipped", childPath, ErrUnsafePath)
				}
				continue
			}
			if info.IsDir() && !candidate {
				if depth >= maxCatalogDepth {
					diagnostic("permission_scan_depth_limit", childPath, fmt.Errorf("key permission scan exceeds %d directory levels", maxCatalogDepth))
					continue
				}
				if err := permissionValidateAncestor(childPath, info); err != nil {
					diagnostic("permission_scan_directory_unsafe", childPath, err)
					continue
				}
				child, _, err := safefile.OpenChildRoot(directory, name)
				if err != nil {
					diagnostic("permission_scan_directory_unsafe", childPath, err)
					continue
				}
				err = walk(child, childPath, depth+1)
				_ = child.Close()
				if err != nil {
					return err
				}
				if stopped {
					return nil
				}
				continue
			}
			if !candidate {
				continue
			}
			if !info.Mode().IsRegular() {
				diagnostic("permission_scan_key_type_unsafe", childPath, ErrUnsafePath)
				continue
			}
			public := strings.HasSuffix(strings.ToLower(name), ".pub")
			kind := "private_key"
			if public {
				kind = "public_key"
			}
			snapshot, candidateFile, err := s.observePermissionTarget(permissionTarget{path: childPath, kind: kind, public: public})
			if candidateFile != nil {
				_ = candidateFile.Close()
			}
			if err != nil {
				diagnostic("permission_scan_key_unsafe", childPath, err)
				continue
			}
			if !sameSelectedKeyFileInfo(info, snapshot.info) {
				diagnostic("permission_scan_source_changed", childPath, ErrSourceChanged)
				return ErrSourceChanged
			}
			observed = append(observed, snapshot)
			selected[childPath] = true
			if len(selected) > maxCatalogFiles {
				delete(selected, childPath)
				diagnostic("permission_scan_key_limit", path, fmt.Errorf("key permission scan exceeds %d key paths", maxCatalogFiles))
				stopped = true
				return nil
			}
		}
		return nil
	}
	walkErr := walk(root, s.paths.SSHDir, 0)
	if walkErr == nil {
		for _, snapshot := range observed {
			if err := ctx.Err(); err != nil {
				walkErr = err
				break
			}
			current, file, err := s.observePermissionTarget(snapshot.target)
			if file != nil {
				_ = file.Close()
			}
			if err != nil || !samePermissionSnapshot(snapshot, current) {
				diagnostic("permission_scan_source_changed", snapshot.target.path, ErrSourceChanged)
				walkErr = ErrSourceChanged
				break
			}
		}
	}
	for path := range selected {
		// Reviewing a .pub path already includes its private companion.
		if selected[path+".pub"] {
			continue
		}
		report.KeyPaths = append(report.KeyPaths, path)
	}
	sort.Strings(report.KeyPaths)
	if walkErr != nil {
		report.Complete = false
		return report, walkErr
	}
	if err := ctx.Err(); err != nil {
		report.Complete = false
		return report, err
	}
	return report, nil
}
