//go:build windows

package safefile

import "os"

func openReadOnly(path string) (*os.File, error) {
	return os.Open(path)
}
