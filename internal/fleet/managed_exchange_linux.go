//go:build linux

package fleet

import (
	"os"

	"golang.org/x/sys/unix"
)

func exchangeManagedUnixFiles(directory *os.File, first, second string) error {
	fd := int(directory.Fd())
	return unix.Renameat2(fd, first, fd, second, unix.RENAME_EXCHANGE)
}

func renameManagedUnixNoReplace(directory *os.File, source, target string) error {
	fd := int(directory.Fd())
	return unix.Renameat2(fd, source, fd, target, unix.RENAME_NOREPLACE)
}
