// Package privatefile protects private state on Unix and Windows.
package privatefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// EnsureDir creates private directories without following user-controlled links.
func EnsureDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		parent := filepath.Dir(path)
		if parent == path {
			return err
		}
		if _, e := os.Lstat(parent); errors.Is(e, fs.ErrNotExist) {
			if e = EnsureDir(parent); e != nil {
				return e
			}
		}
		if err = MakeDir(path); err != nil && !errors.Is(err, fs.ErrExist) {
			return errors.New("cannot create private directory")
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe private directory")
	}
	return Check(path, info, true)
}
