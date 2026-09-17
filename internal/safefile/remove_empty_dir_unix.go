//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package safefile

import (
	"context"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func removeEmptyChildDirNative(ctx context.Context, parent *os.Root, name string, expected fs.FileInfo) error {
	file, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	held, err := file.Stat()
	if err != nil {
		return err
	}
	root, err := parent.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(held, root) || !held.IsDir() {
		return ErrChanged
	}
	if err := VerifyChildRoot(parent, name, expected); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	// Never fall back to unlink: a file substituted after the last check must
	// survive. A directory substituted by a raw writer in this narrow interval
	// cannot be compared atomically with rmdir on Unix.
	return unix.Unlinkat(int(file.Fd()), name, unix.AT_REMOVEDIR)
}
