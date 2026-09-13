//go:build !unix

package platformfs

func Resolve(path string) (Anchor, error) { return filesystemRoot(path), nil }
