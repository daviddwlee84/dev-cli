//go:build unix

package sshhost

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"syscall"

	"golang.org/x/sys/unix"
)

type fileMetadata struct {
	mode   fs.FileMode
	uid    int
	gid    int
	xattrs map[string][]byte
	flags  uint32
}

func platformRejectReparsePath(path string, allowMissingFinal bool) error {
	info, err := os.Lstat(path)
	if allowMissingFinal && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink: %w", path, ErrUnsafePath)
	}
	return nil
}

func platformValidateHome(path string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s has no Unix ownership metadata", path)
	}
	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is not owned by the current user: %w", path, ErrUnsafePath)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is group/world writable: %w", path, ErrUnsafePath)
	}
	return nil
}

func platformValidatePrivateDirectory(path string, info fs.FileInfo) error {
	if err := platformValidateHome(path, info); err != nil {
		return err
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("mode is %04o, want 0700: %w", info.Mode().Perm(), ErrUnsafePath)
	}
	return nil
}

func platformMakePrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func platformOpenNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func platformValidatePrivateFile(path string, file *os.File, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s has no Unix ownership metadata", path)
	}
	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is not owned by the current user: %w", path, ErrUnsafePath)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("%s has %d hard links, want 1: %w", path, stat.Nlink, ErrUnsafePath)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%s mode is %04o, want 0600: %w", path, info.Mode().Perm(), ErrUnsafePath)
	}
	return nil
}

func platformCaptureMetadata(path string, file *os.File, info fs.FileInfo) (fileMetadata, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileMetadata{}, errors.New("file has no Unix ownership metadata")
	}
	xattrs, err := platformReadXattrs(file)
	if err != nil {
		return fileMetadata{}, err
	}
	flags, err := platformFileFlags(file)
	if err != nil {
		return fileMetadata{}, err
	}
	return fileMetadata{
		mode: info.Mode().Perm(), uid: int(stat.Uid), gid: int(stat.Gid), xattrs: xattrs, flags: flags,
	}, nil
}

func platformMetadataRoundTrip(metadata fileMetadata) error {
	if metadata.uid != os.Geteuid() {
		return errors.New("owner is not the current user")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return fmt.Errorf("resolve current groups: %w", err)
	}
	groupAllowed := metadata.gid == os.Getegid()
	for _, group := range groups {
		groupAllowed = groupAllowed || metadata.gid == group
	}
	if !groupAllowed {
		return fmt.Errorf("group %d cannot be restored by the current user", metadata.gid)
	}
	if err := platformFlagsRoundTrip(metadata.flags); err != nil {
		return err
	}
	for name := range metadata.xattrs {
		// Kernel security labels and POSIX ACLs often require privilege to set and
		// can grant access beyond mode 0600. Block before staging rather than
		// silently dropping or approximating them.
		if len(name) >= 9 && name[:9] == "security." || len(name) >= 7 && name[:7] == "system." || name == "com.apple.system.Security" {
			return fmt.Errorf("extended attribute %q requires manual preservation", name)
		}
	}
	return nil
}

func platformApplyPrivateFile(path string, file *os.File) error {
	return file.Chmod(0o600)
}

func platformApplyMetadata(path string, file *os.File, metadata fileMetadata) error {
	if err := file.Chown(metadata.uid, metadata.gid); err != nil {
		return err
	}
	if err := file.Chmod(metadata.mode.Perm()); err != nil {
		return err
	}
	if err := platformWriteXattrs(file, metadata.xattrs); err != nil {
		return err
	}
	return platformSetFileFlags(file, metadata.flags)
}

func platformVerifyMetadata(path string, file *os.File, expected fileMetadata) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	actual, err := platformCaptureMetadata(path, file, info)
	if err != nil {
		return err
	}
	if actual.mode != expected.mode || actual.uid != expected.uid || actual.gid != expected.gid || actual.flags != expected.flags || !reflect.DeepEqual(actual.xattrs, expected.xattrs) {
		return errors.New("staged Unix metadata differs from source metadata")
	}
	return nil
}

func platformAtomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}

func platformSyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
