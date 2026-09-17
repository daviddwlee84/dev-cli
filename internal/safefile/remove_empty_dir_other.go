//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package safefile

import (
	"context"
	"errors"
	"io/fs"
	"os"
)

func removeEmptyChildDirNative(context.Context, *os.Root, string, fs.FileInfo) error {
	return errors.New("directory-only removal is not supported on this platform")
}
