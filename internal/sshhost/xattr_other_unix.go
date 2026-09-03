//go:build unix && !darwin && !linux

package sshhost

import (
	"errors"
	"os"
)

func platformReadXattrs(*os.File) (map[string][]byte, error) {
	return nil, errors.New("extended-attribute round-trip is unsupported on this Unix platform")
}

func platformWriteXattrs(*os.File, map[string][]byte) error {
	return errors.New("extended-attribute round-trip is unsupported on this Unix platform")
}
