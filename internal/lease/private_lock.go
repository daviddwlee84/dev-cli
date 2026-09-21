package lease

import (
	"errors"
	"os"
	"path/filepath"
)

func privateLockParent(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("private lock parent is not a directory")
	}
	private, err := privateModeMatches(parent, info.Mode(), 0700)
	if err != nil {
		return err
	}
	if !private {
		return errors.New("private lock parent has an unprotected policy")
	}
	return nil
}
