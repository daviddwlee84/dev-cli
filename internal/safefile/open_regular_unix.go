//go:build darwin || linux

package safefile

import (
	"os"
	"syscall"
)

func openRegularForRead(root *os.Root, name string) (*os.File, error) {
	// O_NONBLOCK prevents a regular-to-FIFO rename race from hanging the
	// process. O_NOFOLLOW rejects a final-component link before post-open
	// identity checks run.
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
