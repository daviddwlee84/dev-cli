//go:build !unix

package experiment

import "os"

func openGraduateRegular(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
