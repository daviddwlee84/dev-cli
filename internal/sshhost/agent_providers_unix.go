//go:build unix

package sshhost

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func platformAgentDirectory(path string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() && stat.Uid != 0 {
		return fmt.Errorf("%s has untrusted directory ownership: %w", path, ErrUnsafePath)
	}
	if info.Mode().Perm()&0o022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
		return fmt.Errorf("%s is a writable agent directory: %w", path, ErrUnsafePath)
	}
	return nil
}

func platformAgentSocket(path string, info fs.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !agentSocketMode(info) {
		return fmt.Errorf("%s is not a socket: %w", path, ErrUnsafePath)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is not owned by the current user: %w", path, ErrUnsafePath)
	}
	return nil
}
