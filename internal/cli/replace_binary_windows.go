//go:build windows

package cli

import (
	"os"
	"path/filepath"
)

// replaceBinary swaps target for newPath. Windows will not let a running .exe be
// renamed over, so the live binary is moved aside first; the leftover ".old" is
// removed here if possible and otherwise on the next run.
func replaceBinary(newPath, target string) error {
	old := target + ".old"
	_ = os.Remove(old)
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(newPath, target); err != nil {
		_ = os.Rename(old, target)
		return err
	}
	_ = os.Remove(old)
	return nil
}

// sweepStaleUpgradeArtifacts deletes a ".old" binary left by a previous upgrade
// once the new process is running and the old file is no longer locked.
func sweepStaleUpgradeArtifacts() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	_ = os.Remove(self + ".old")
}
