//go:build unix

package lease

import (
	"errors"
	"os"

	"github.com/daviddwlee84/dev-cli/internal/privatefile"
)

func protectPrivateLock(file *os.File) error {
	if err := privateLockParent(file.Name()); err != nil {
		return err
	}
	held, err := file.Stat()
	if err != nil {
		return err
	}
	named, err := os.Lstat(file.Name())
	if err != nil {
		return err
	}
	if !held.Mode().IsRegular() || held.Size() != 0 || named.Mode()&os.ModeSymlink != 0 || !os.SameFile(held, named) {
		return errors.New("private lock identity or contents changed")
	}
	if err := privatefile.Check(file.Name(), held, false); err != nil {
		return err
	}
	private, err := privateModeMatches(file.Name(), held.Mode(), 0600)
	if err != nil {
		return err
	}
	if !private {
		return errors.New("private lock permissions are not owner-only")
	}
	return nil
}
