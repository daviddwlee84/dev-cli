//go:build !windows

package claudeworkflow

import (
	"io/fs"
	"os"
)

func safeMetadataInfo(_ string, info fs.FileInfo, wantDir bool) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	if wantDir {
		return info.IsDir()
	}
	return info.Mode().IsRegular()
}
