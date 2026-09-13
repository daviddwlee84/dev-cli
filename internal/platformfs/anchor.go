// Package platformfs supplies narrowly scoped native filesystem capabilities.
package platformfs

import (
	"path/filepath"
	"regexp"
	"runtime"
)

// Anchor is a verified starting directory for a guarded path walk. Android
// apps can stat their system ancestors but cannot open them for directory reads.
// Verify rechecks the complete platform prefix; callers still guard every
// component below Path and revalidate their held directory/file identities.
type Anchor struct {
	Path   string
	verify func() error
}

func (a Anchor) Verify() error {
	if a.verify != nil {
		return a.verify()
	}
	return nil
}

func filesystemRoot(path string) Anchor {
	return Anchor{Path: filepath.VolumeName(path) + string(filepath.Separator)}
}

var appDataLabel = regexp.MustCompile(`^u:object_r:app_data_file:s0(:c[0-9]+(,c[0-9]+)*)?\x00?$`)

// KernelLabel recognizes Android's inherited app-data label. It may be observed
// and compared, never restored with setxattr or omitted from identity checks.
func KernelLabel(name string, value []byte) bool {
	return kernelLabel(runtime.GOOS, name, value)
}

func kernelLabel(goos, name string, value []byte) bool {
	return goos == "android" && name == "security.selinux" && len(value) <= 4096 && appDataLabel.Match(value)
}

// InheritedFileFlags reports read-only flags supplied by Android's filesystem.
// FS_ENCRYPT_FL describes transparent file encryption; dev never sets/clears it.
func InheritedFileFlags() uint32 {
	if runtime.GOOS == "android" {
		return 0x00000800
	}
	return 0
}
