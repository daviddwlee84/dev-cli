//go:build windows

package experiment

import "os"

func renameExclusive(source, destination string) error {
	if err := rejectExisting(destination); err != nil {
		return err
	}
	return os.Rename(source, destination)
}
