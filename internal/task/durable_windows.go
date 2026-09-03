//go:build windows

package task

import "golang.org/x/sys/windows"

func replaceFile(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// MoveFileEx with MOVEFILE_WRITE_THROUGH durably publishes the replacement.
// Windows does not expose a portable directory fsync through os.File.
func syncDirectory(string) error { return nil }
