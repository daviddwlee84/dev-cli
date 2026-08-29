//go:build !windows

package cli

import "os"

// replaceBinary swaps target for the freshly downloaded newPath. On POSIX a
// running executable can be replaced by an atomic rename on the same
// filesystem, and the process keeps running from the now-unlinked inode.
func replaceBinary(newPath, target string) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if err := os.Chmod(newPath, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(newPath, target)
}

// sweepStaleUpgradeArtifacts is a no-op on POSIX; only Windows leaves a
// ".old" file behind.
func sweepStaleUpgradeArtifacts() {}
