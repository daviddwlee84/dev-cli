//go:build !darwin && !linux

package safefile

import "os"

func openRegularForRead(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
