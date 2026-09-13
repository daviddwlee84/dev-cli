//go:build !android

package platformfs

import "os"

func publishNoReplace(root *os.Root, stage, destination string) (bool, error) {
	return false, root.Link(stage, destination)
}
