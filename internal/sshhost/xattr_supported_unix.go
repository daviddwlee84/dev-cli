//go:build darwin || linux

package sshhost

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

func platformReadXattrs(file *os.File) (map[string][]byte, error) {
	fd := int(file.Fd())
	size, err := unix.Flistxattr(fd, nil)
	if err != nil {
		return nil, fmt.Errorf("list extended attributes: %w", err)
	}
	if size == 0 {
		return map[string][]byte{}, nil
	}
	buffer := make([]byte, size)
	size, err = unix.Flistxattr(fd, buffer)
	if err != nil {
		return nil, fmt.Errorf("list extended attributes: %w", err)
	}
	attributes := make(map[string][]byte)
	for _, name := range strings.Split(string(buffer[:size]), "\x00") {
		if name == "" {
			continue
		}
		valueSize, err := unix.Fgetxattr(fd, name, nil)
		if err != nil {
			return nil, fmt.Errorf("read extended attribute %q: %w", name, err)
		}
		value := make([]byte, valueSize)
		if valueSize > 0 {
			read, err := unix.Fgetxattr(fd, name, value)
			if err != nil {
				return nil, fmt.Errorf("read extended attribute %q: %w", name, err)
			}
			value = value[:read]
		}
		attributes[name] = value
	}
	return attributes, nil
}

func platformWriteXattrs(file *os.File, attributes map[string][]byte) error {
	fd := int(file.Fd())
	names := make([]string, 0, len(attributes))
	for name := range attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := unix.Fsetxattr(fd, name, attributes[name], 0); err != nil {
			return fmt.Errorf("restore extended attribute %q: %w", name, err)
		}
	}
	return nil
}
