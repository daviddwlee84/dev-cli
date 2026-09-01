//go:build !darwin && !linux

package safefile

import "io/fs"

func samePlatformState(fs.FileInfo, fs.FileInfo) bool { return true }
