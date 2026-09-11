//go:build linux

package sshcredential

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
)

func brokerPeerAllowed(c net.Conn) error {
	raw, err := c.(*net.UnixConn).SyscallConn()
	if err != nil {
		return err
	}
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		u, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil || int(u.Uid) != os.Geteuid() {
			peerErr = ErrDenied
		}
	})
	if err != nil {
		return err
	}
	return peerErr
}
