//go:build windows

package bootstrap

import (
	"path/filepath"
	"strings"
)

func sameFilesystem(a, b string) bool {
	return strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b))
}
