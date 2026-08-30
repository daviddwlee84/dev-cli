package scaffold

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// ExistingPolicy controls a rendered file whose destination already contains
// different content. Identical content is always an idempotent no-op.
type ExistingPolicy string

const (
	ExistingError     ExistingPolicy = "error"
	ExistingSkip      ExistingPolicy = "skip"
	ExistingOverwrite ExistingPolicy = "overwrite"
)

// WriteResult records what ApplyFiles did for one planned file.
type WriteResult struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Action string `json:"action"` // created, overwritten, unchanged, or skipped
}

// ApplyFiles safely materializes the file portion of a plan. It revalidates
// every path immediately before writing and rejects final-component symlinks.
// Other plan actions remain entirely caller-owned.
func ApplyFiles(plan Plan, policy ExistingPolicy) ([]WriteResult, error) {
	switch policy {
	case "", ExistingError:
		policy = ExistingError
	case ExistingSkip, ExistingOverwrite:
	default:
		return nil, fmt.Errorf("unknown existing-file policy %q", policy)
	}
	if plan.Root == "" {
		return nil, fmt.Errorf("plan has no root")
	}
	if err := os.MkdirAll(plan.Root, 0o755); err != nil {
		return nil, fmt.Errorf("create scaffold root %q: %w", plan.Root, err)
	}
	root, err := pathx.Canonical(plan.Root)
	if err != nil {
		return nil, fmt.Errorf("canonicalize scaffold root: %w", err)
	}

	// Preflight known conflicts before creating any file. This does not pretend
	// to be a transaction, but avoids a half-written preset for ordinary errors.
	for _, file := range plan.Files {
		path, info, current, inspectErr := inspectDestination(root, file)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if info == nil || bytes.Equal(current, []byte(file.Content)) {
			continue
		}
		if policy == ExistingError {
			return nil, fmt.Errorf("file %q destination %q already exists with different content", file.ID, path)
		}
	}

	results := make([]WriteResult, 0, len(plan.Files))
	for _, file := range plan.Files {
		path, info, current, inspectErr := inspectDestination(root, file)
		if inspectErr != nil {
			return results, inspectErr
		}
		if info != nil && bytes.Equal(current, []byte(file.Content)) {
			results = append(results, WriteResult{ID: file.ID, Path: path, Action: "unchanged"})
			continue
		}
		if info != nil && policy == ExistingSkip {
			results = append(results, WriteResult{ID: file.ID, Path: path, Action: "skipped"})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return results, fmt.Errorf("create parent for file %q: %w", file.ID, err)
		}
		// A newly encountered parent may itself be a symlink; resolve again after
		// MkdirAll and before placing the temporary file.
		_, revalidated, err := safeDestination(root, file.RelativePath)
		if err != nil || revalidated != path {
			if err == nil {
				err = ErrUnsafePath
			}
			return results, fmt.Errorf("file %q destination changed during apply: %w", file.ID, err)
		}
		if err := atomicWrite(path, []byte(file.Content), file.Mode, info != nil); err != nil {
			return results, fmt.Errorf("write file %q to %q: %w", file.ID, path, err)
		}
		action := "created"
		if info != nil {
			action = "overwritten"
		}
		results = append(results, WriteResult{ID: file.ID, Path: path, Action: action})
	}
	return results, nil
}

func inspectDestination(root string, file FilePlan) (string, fs.FileInfo, []byte, error) {
	relative, path, err := safeDestination(root, file.RelativePath)
	if err != nil {
		return "", nil, nil, fmt.Errorf("file %q: %w", file.ID, err)
	}
	if relative != file.RelativePath || path != file.Path {
		return "", nil, nil, fmt.Errorf("file %q planned destination no longer matches root: %w", file.ID, ErrUnsafePath)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil, nil, nil
	}
	if err != nil {
		return "", nil, nil, fmt.Errorf("inspect file %q destination: %w", file.ID, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, nil, fmt.Errorf("file %q destination is a symlink: %w", file.ID, ErrUnsafePath)
	}
	if !info.Mode().IsRegular() {
		return "", nil, nil, fmt.Errorf("file %q destination is not a regular file", file.ID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, nil, fmt.Errorf("read file %q destination: %w", file.ID, err)
	}
	return path, info, data, nil
}

func atomicWrite(destination string, data []byte, mode fs.FileMode, replacing bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".dev-scaffold-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keepTemp := true
	defer func() {
		_ = tmp.Close()
		if keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !replacing {
		if _, err := os.Lstat(destination); !errors.Is(err, fs.ErrNotExist) {
			if err == nil {
				return fs.ErrExist
			}
			return err
		}
	}
	if err := os.Rename(tmpPath, destination); err == nil {
		keepTemp = false
		return nil
	} else if !replacing {
		return err
	}

	// os.Rename does not replace an existing file on every supported platform.
	// Fall back to a same-directory backup with rollback rather than deleting
	// the old contents before the new file has a destination.
	backup, err := os.CreateTemp(filepath.Dir(destination), ".dev-scaffold-backup-*")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(destination, backupPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		_ = os.Rename(backupPath, destination)
		return err
	}
	keepTemp = false
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	return nil
}
