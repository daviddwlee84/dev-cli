package experiment

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func removalIdentity(path string) (string, string, error) {
	var info unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_INO|unix.STATX_MNT_ID|unix.STATX_BTIME, &info); err != nil {
		return "", "", fmt.Errorf("inspect removal filesystem identity: %w", err)
	}
	if info.Mask&unix.STATX_MNT_ID == 0 {
		return "", "", fmt.Errorf("Try removal requires statx mount identity support")
	}
	// Device numbers alone cannot detect same-filesystem bind mounts. Walking
	// across one could delete a directory outside the intended Try.
	identity := fmt.Sprintf("%d:%d:%d:%d:%d", info.Dev_major, info.Dev_minor, info.Ino, info.Btime.Sec, info.Btime.Nsec)
	return identity, fmt.Sprint(info.Mnt_id), nil
}
