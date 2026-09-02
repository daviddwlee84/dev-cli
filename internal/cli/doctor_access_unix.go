//go:build unix

package cli

import "golang.org/x/sys/unix"

// dirWritable asks the kernel whether the current process may write in the
// directory. Unlike the former temp-file probe, it has no filesystem effects.
func dirWritable(dir string) bool {
	return unix.Access(dir, unix.W_OK) == nil
}
