//go:build linux || darwin

package configedit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"golang.org/x/sys/unix"
)

// Metadata is private receipt material, never part of a public edit plan.
type Metadata struct {
	Present    bool              `json:"present"`
	Group      int               `json:"group"`
	Attributes map[string][]byte `json:"attributes,omitempty"`
}

func checkAncestor(_ string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("missing directory ownership")
	}
	if int(stat.Uid) != 0 && int(stat.Uid) != os.Geteuid() {
		return errors.New("configuration ancestor belongs to another user")
	}
	if info.Mode().Perm()&0o022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
		return errors.New("configuration ancestor is writable by others")
	}
	return nil
}
func checkDirectory(_ string, info fs.FileInfo) error {
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(s.Uid) != os.Geteuid() || info.Mode().Perm()&0o022 != 0 {
		return errors.New("configuration parent must be user-owned and not writable by others")
	}
	return nil
}
func captureMetadata(path string, info fs.FileInfo) (Metadata, error) {
	var m Metadata
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(s.Uid) != os.Geteuid() || s.Nlink != 1 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode().Perm()&0o022 != 0 {
		return m, errors.New("configuration metadata requires manual preservation (owner, links or permissions)")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return m, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return m, err
	}
	if !safefile.SameFileState(info, opened) {
		return m, ErrStale
	}
	attrs, err := readAttributes(f)
	if err != nil {
		return m, err
	}
	m = Metadata{Present: true, Group: int(s.Gid), Attributes: attrs}
	if err = m.validate(); err != nil {
		return m, err
	}
	if err = checkFlags(f, opened); err != nil {
		return m, err
	}
	return m, nil
}
func (m Metadata) validate() error {
	if !m.Present {
		return nil
	}
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	allowed := m.Group == os.Getegid()
	for _, g := range groups {
		allowed = allowed || m.Group == g
	}
	if !allowed {
		return errors.New("configuration group requires manual preservation")
	}
	total := 0
	for name, value := range m.Attributes {
		total += len(name) + len(value)
		if strings.HasPrefix(name, "security.") || strings.HasPrefix(name, "system.") || name == "com.apple.system.Security" {
			return errors.New("configuration security attributes require manual preservation")
		}
	}
	if total > maxBytes {
		return errors.New("configuration metadata exceeds byte limit")
	}
	return nil
}
func readAttributes(file *os.File) (map[string][]byte, error) {
	fd := int(file.Fd())
	size, err := unix.Flistxattr(fd, nil)
	if err == unix.ENOTSUP {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if size > maxBytes {
		return nil, errors.New("too many extended attributes")
	}
	names := make([]byte, size)
	if size > 0 {
		size, err = unix.Flistxattr(fd, names)
		if err != nil {
			return nil, err
		}
	}
	// Empty attributes are omitted from recovery JSON and decode as nil.
	// Keep live observations in that same canonical form so a receipt does
	// not mistake an unchanged file for an external metadata replacement.
	var attrs map[string][]byte
	total := 0
	for _, name := range strings.Split(string(names[:size]), "\x00") {
		if name == "" {
			continue
		}
		n, err := unix.Fgetxattr(fd, name, nil)
		if err != nil {
			return nil, err
		}
		total += n
		if total > maxBytes {
			return nil, errors.New("extended attributes exceed byte limit")
		}
		value := make([]byte, n)
		if n > 0 {
			n, err = unix.Fgetxattr(fd, name, value)
			if err != nil {
				return nil, err
			}
			value = value[:n]
		}
		if attrs == nil {
			attrs = make(map[string][]byte)
		}
		attrs[name] = value
	}
	return attrs, nil
}
func (m Metadata) prepare(file *os.File) error {
	if !m.Present {
		attrs, err := readAttributes(file)
		if err != nil {
			return err
		}
		return (Metadata{Present: true, Group: os.Getegid(), Attributes: attrs}).validate()
	}
	if err := m.validate(); err != nil {
		return err
	}
	if err := file.Chown(-1, m.Group); err != nil {
		return err
	}
	current, err := readAttributes(file)
	if err != nil {
		return err
	}
	if err := (Metadata{Present: true, Group: m.Group, Attributes: current}).validate(); err != nil {
		return err
	}
	for name, value := range m.Attributes {
		if prior, ok := current[name]; ok && bytes.Equal(prior, value) {
			continue
		}
		if err = unix.Fsetxattr(int(file.Fd()), name, value, 0); err != nil {
			return fmt.Errorf("restore configuration attribute %s: %w", name, err)
		}
	}
	after, err := readAttributes(file)
	if err != nil {
		return err
	}
	for name, value := range m.Attributes {
		if actual, ok := after[name]; !ok || !bytes.Equal(actual, value) {
			return errors.New("configuration metadata did not round-trip")
		}
	}
	return nil
}

func sameDevice(_, _ string, a, b fs.FileInfo) bool {
	left, lok := a.Sys().(*syscall.Stat_t)
	right, rok := b.Sys().(*syscall.Stat_t)
	return lok && rok && left.Dev == right.Dev
}

func fileIdentity(_ string, info fs.FileInfo) string {
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
}

func makeDirectory(path string) error                     { return os.Mkdir(path, 0o700) }
func privateRecoveryMode(_ string, info fs.FileInfo) bool { return info.Mode().Perm() == 0o700 }
