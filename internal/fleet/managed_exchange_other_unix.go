//go:build unix && !darwin && !linux

package fleet

import (
	"fmt"
	"os"
)

func exchangeManagedUnixFiles(_ *os.File, _, _ string) error {
	return fmt.Errorf("atomic filename exchange is unavailable on this platform: %w", ErrManagedSecurityBackend)
}

func renameManagedUnixNoReplace(_ *os.File, _, _ string) error {
	return fmt.Errorf("atomic no-replace rename is unavailable on this platform: %w", ErrManagedSecurityBackend)
}
