//go:build darwin || linux

package sshhost

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type permissionMetadata struct {
	uid        uint32
	gid        uint32
	flags      uint32
	attributes map[string]string
}

func permissionValidateAncestor(path string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("permission parent %s must be a direct current-user-owned directory: %w", path, ErrUnsafePath)
	}
	return nil
}

func permissionOpenFile(root *os.Root, name string, directory bool) (*os.File, error) {
	flags := os.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	return root.OpenFile(name, flags, 0)
}

func permissionReadMetadata(path string, file *os.File, info fs.FileInfo, target permissionTarget) (permissionMetadata, error) {
	var metadata permissionMetadata
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return metadata, fmt.Errorf("permission target %s is not owned by the current user: %w", path, ErrUnsafePath)
	}
	if !target.directory && stat.Nlink != 1 {
		return metadata, fmt.Errorf("permission target has multiple hard links: %w", ErrUnsafePath)
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return metadata, fmt.Errorf("special permission bits require manual remediation: %w", ErrManualRemediation)
	}
	metadata.uid, metadata.gid = stat.Uid, stat.Gid
	flags, err := platformFileFlags(file)
	if err != nil {
		return metadata, err
	}
	if err := platformFlagsRoundTrip(flags); err != nil {
		return metadata, fmt.Errorf("permission flags require manual remediation: %w", err)
	}
	metadata.flags = flags
	if err := permissionCheckACL(file); err != nil {
		return metadata, err
	}
	attributes, err := permissionAttributes(file)
	if err != nil {
		return metadata, err
	}
	metadata.attributes = attributes
	return metadata, nil
}

// Read only bounded filesystem metadata, retaining hashes rather than values.
// Security/ACL attributes must be understood by a separate explicit workflow;
// this repair never removes or rewrites them.
func permissionAttributes(file *os.File) (map[string]string, error) {
	const maxMetadata = 256 << 10
	fd := int(file.Fd())
	length, err := unix.Flistxattr(fd, nil)
	if errors.Is(err, unix.ENOTSUP) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if length > maxMetadata {
		return nil, fmt.Errorf("permission metadata exceeds bounds: %w", ErrManualRemediation)
	}
	names := make([]byte, length)
	if length > 0 {
		length, err = unix.Flistxattr(fd, names)
		if err != nil {
			return nil, err
		}
	}
	attributes := map[string]string{}
	total := length
	for _, name := range strings.Split(string(names[:length]), "\x00") {
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, "security.") || strings.HasPrefix(name, "system.") || name == "com.apple.system.Security" {
			return nil, fmt.Errorf("security attributes or ACLs require manual remediation: %w", ErrManualRemediation)
		}
		size, err := unix.Fgetxattr(fd, name, nil)
		if err != nil {
			return nil, err
		}
		total += size
		if total > maxMetadata {
			return nil, fmt.Errorf("permission metadata exceeds bounds: %w", ErrManualRemediation)
		}
		data := make([]byte, size)
		if size > 0 {
			size, err = unix.Fgetxattr(fd, name, data)
			if err != nil {
				return nil, err
			}
		}
		digest := sha256.Sum256(data[:size])
		attributes[name] = hex.EncodeToString(digest[:])
	}
	return attributes, nil
}

func permissionDesiredMode(snapshot permissionSnapshot) (fs.FileMode, error) {
	before := snapshot.info.Mode().Perm()
	desired := fs.FileMode(0o600)
	if snapshot.target.directory {
		desired = 0o700
	}
	if snapshot.target.public {
		desired = before &^ 0o022
	}
	if desired&^before != 0 {
		return before, fmt.Errorf("repair would add owner permissions; adjust this path manually: %w", ErrManualRemediation)
	}
	return desired, nil
}

func permissionSameOwner(a, b fs.FileInfo) bool {
	left, lok := a.Sys().(*syscall.Stat_t)
	right, rok := b.Sys().(*syscall.Stat_t)
	return lok && rok && left.Uid == right.Uid && left.Gid == right.Gid
}

func permissionSameNonModeMetadata(a, b permissionMetadata) bool { return reflect.DeepEqual(a, b) }
func permissionChmod(file *os.File, mode fs.FileMode) error      { return file.Chmod(mode) }
func permissionSync(file *os.File) error                         { return file.Sync() }
