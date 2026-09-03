//go:build darwin

package sshhost

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func platformFileFlags(file *os.File) (uint32, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return 0, err
	}
	return stat.Flags, nil
}

func platformFlagsRoundTrip(flags uint32) error {
	const immutable = unix.UF_IMMUTABLE | unix.UF_APPEND | unix.SF_IMMUTABLE | unix.SF_APPEND
	if flags&immutable != 0 {
		return fmt.Errorf("immutable or append-only file flags 0x%x prevent safe replacement", flags&immutable)
	}
	return nil
}

func platformSetFileFlags(file *os.File, flags uint32) error {
	return unix.Fchflags(int(file.Fd()), int(flags))
}
