package machineregistry

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func inspectSource(path string) (sourceState, error) {
	source, err := inspectParents(filepath.Dir(path), false)
	if err != nil {
		return source, err
	}
	source.database, err = inspectPrivateFile(path, true)
	if err != nil {
		return source, err
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return source, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxDatabaseBytes {
			return source, ErrUnsafePath
		}
		if err := checkAuxiliary(path+suffix, info); err != nil {
			return source, err
		}
	}
	if source.database != nil && source.database.Size() > 0 {
		if err := inspectHeader(path, source.database); err != nil {
			return source, err
		}
	}
	return source, nil
}

func inspectHeader(path string, expected fs.FileInfo) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(expected, opened) {
		return errors.Join(ErrStale, err)
	}
	header := make([]byte, 100)
	if _, err := io.ReadFull(file, header); err != nil || string(header[:16]) != "SQLite format 3\x00" {
		return fmt.Errorf("invalid machine registry database header: %w", ErrSchema)
	}
	// WAL readers can create shared-memory files. This low-write-volume store
	// deliberately uses rollback journals so read-only list never does that.
	if header[18] != 1 || header[19] != 1 {
		return fmt.Errorf("machine registry requires rollback journaling: %w", ErrSchema)
	}
	return verifyDatabaseIdentity(path, expected)
}

func inspectPrivateFile(path string, allowMissing bool) (fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if allowMissing && errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxDatabaseBytes {
		return nil, fmt.Errorf("registry file must be a bounded regular file: %w", ErrUnsafePath)
	}
	if err := checkPrivate(path, info, 0o600); err != nil {
		return nil, err
	}
	return info, nil
}

func inspectParents(path string, create bool) (sourceState, error) {
	state := sourceState{}
	root := filepath.VolumeName(path) + string(filepath.Separator)
	current := root
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return state, err
	}
	state.anchor, state.identity = root, rootInfo
	parts := strings.Split(strings.TrimPrefix(path, root), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		created := false
		if errors.Is(err, fs.ErrNotExist) {
			if !create {
				return state, nil
			}
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return state, err
			} else if err == nil {
				created = true
			}
			if created {
				if err := setPrivateMode(current, 0o700); err != nil {
					return state, err
				}
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return state, err
		}
		// These OS-owned aliases precede all user-controlled path components.
		if runtime.GOOS == "darwin" && (current == "/var" || current == "/tmp") && info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(current)
			if err == nil && (target == "private"+current || target == "/private"+current) {
				continue
			}
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return state, fmt.Errorf("registry parent is not a real directory: %w", ErrUnsafePath)
		}
		if err := checkAncestor(current, info); err != nil {
			return state, err
		}
		if current == path {
			if err := checkPrivate(current, info, 0o700); err != nil {
				return state, err
			}
		}
		state.anchor, state.identity = current, info
	}
	return state, nil
}

func prepareDirectory(path string) error {
	_, err := inspectParents(path, true)
	return err
}

func preparePrivateFile(path string) (fs.FileInfo, error) {
	info, err := inspectPrivateFile(path, true)
	if err != nil || info != nil {
		return info, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return inspectPrivateFile(path, false)
	}
	if err != nil {
		return nil, err
	}
	privateErr := setPrivateMode(path, 0o600)
	closeErr := file.Close()
	if err := errors.Join(privateErr, closeErr); err != nil {
		return nil, err
	}
	return inspectPrivateFile(path, false)
}

func verifyAnchor(source sourceState) error {
	if source.anchor == "" || source.identity == nil {
		return ErrInvalidPlan
	}
	current, err := os.Lstat(source.anchor)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(source.identity, current) {
		return errors.Join(ErrStale, err)
	}
	return nil
}

func verifyDatabaseIdentity(path string, expected fs.FileInfo) error {
	current, err := inspectPrivateFile(path, false)
	if err != nil {
		return err
	}
	if expected == nil || !os.SameFile(current, expected) {
		return ErrStale
	}
	return nil
}

func verifySource(path string, source sourceState) error {
	if err := verifyAnchor(source); err != nil {
		return err
	}
	return verifyDatabaseIdentity(path, source.database)
}

func sameSourceIdentity(before, after sourceState) bool {
	if before.database == nil || after.database == nil {
		return before.database == nil && after.database == nil
	}
	return os.SameFile(before.database, after.database)
}
