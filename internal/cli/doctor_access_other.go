//go:build !unix && !windows

package cli

import "os"

// dirWritable is a conservative metadata fallback for unsupported controllers.
func dirWritable(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir() && info.Mode().Perm()&0o222 != 0
}
