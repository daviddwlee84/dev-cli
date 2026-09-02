//go:build darwin

package fleet

import (
	"os"

	"golang.org/x/sys/unix"
)

func exchangeManagedUnixFiles(directory *os.File, first, second string) error {
	fd := int(directory.Fd())
	return unix.RenameatxNp(fd, first, fd, second, unix.RENAME_SWAP)
}

func renameManagedUnixNoReplace(directory *os.File, source, target string) error {
	fd := int(directory.Fd())
	return unix.RenameatxNp(fd, source, fd, target, unix.RENAME_EXCL)
}
