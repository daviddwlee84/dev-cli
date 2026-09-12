//go:build !windows

package sshhost

// Unix change time is already retained by fs.FileInfo.Sys and compared by
// permissionSameChangeTime; unsupported platforms fail that comparison closed.
func selectedKeyNativeChangeTime(secureFileIdentity) (int64, error) { return 0, nil }
