//go:build !windows

package safefile

import "io/fs"

func isReparsePoint(fs.FileInfo) bool { return false }
