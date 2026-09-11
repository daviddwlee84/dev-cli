//go:build unix

package sshcredential

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func listenBroker() (net.Listener, string, func(), error) {
	dir, err := os.MkdirTemp("", "dev-ssh-askpass-")
	if err != nil {
		return nil, "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err = os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return nil, "", nil, err
	}
	path := filepath.Join(dir, "broker.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	if err = os.Chmod(path, 0o600); err != nil {
		l.Close()
		cleanup()
		return nil, "", nil, err
	}
	return l, path, cleanup, nil
}
func dialBroker(ctx context.Context, path string) (net.Conn, error) {
	for _, p := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, ErrDenied
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0 || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrDenied
		}
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}
