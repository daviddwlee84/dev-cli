//go:build darwin

package sshhost

import "golang.org/x/sys/unix"

func platformNoReplace(source, destination string) error {
	return unix.RenamexNp(source, destination, unix.RENAME_EXCL)
}
