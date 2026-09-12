//go:build darwin

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
		u, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil || int(u.Uid) != os.Geteuid() {
			peerErr = ErrDenied
		}
	})
	if err != nil {
		return err
	}
	return peerErr
}
