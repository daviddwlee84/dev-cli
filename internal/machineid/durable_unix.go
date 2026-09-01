//go:build unix

package machineid

import (
	"io/fs"
	"os"
)

func setPrivateMode(path string, want fs.FileMode) error {
	return os.Chmod(path, want.Perm())
}

func privateModeMatches(_ string, mode, want fs.FileMode) (bool, error) {
	return mode.Perm() == want.Perm(), nil
}

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
