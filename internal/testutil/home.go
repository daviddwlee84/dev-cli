package testutil

import (
	"path/filepath"
	"strings"
	"testing"
)

// SetHome isolates both native Windows and POSIX home discovery. Setting HOME
// alone leaves os.UserHomeDir pointed at the runner's real profile on Windows.
func SetHome(t testing.TB, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	drive := filepath.VolumeName(home)
	t.Setenv("HOMEDRIVE", drive)
	t.Setenv("HOMEPATH", strings.TrimPrefix(home, drive))
}
