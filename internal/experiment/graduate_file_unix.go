//go:build unix

package experiment

import (
	"os"

	"golang.org/x/sys/unix"
)

func openGraduateRegular(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}
