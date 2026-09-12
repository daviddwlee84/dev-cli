//go:build !darwin && !linux

package fleet

import (
	"errors"
	"io/fs"
)

func herdrDirectoryIdentity(fs.FileInfo) (string, error) {
	return "", errors.New("Herdr repository preparation supports macOS and Linux targets")
}
