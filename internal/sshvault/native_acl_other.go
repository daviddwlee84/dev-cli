//go:build !darwin && !linux

package sshvault

import "io/fs"

func nativeSameChangeTime(fs.FileInfo, fs.FileInfo) bool { return false }
func captureNativeACL(string, fs.FileInfo) (nativeACLObservation, error) {
	return nativeACLObservation{}, ErrNativeContext
}
