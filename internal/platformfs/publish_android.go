package platformfs

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishNoReplace(root *os.Root, stage, destination string) (bool, error) {
	directory, err := root.Open(".")
	if err != nil {
		return false, err
	}
	defer directory.Close()
	fd := int(directory.Fd())
	err = unix.Renameat2(fd, stage, fd, destination, unix.RENAME_NOREPLACE)
	return err == nil, err
}
