package platformfs

import (
	"errors"
	"os"
	"strings"
)

// PublishNoReplace atomically publishes a staged file within one held directory.
// moved reports whether publication consumed the staging name. Android uses
// RENAME_NOREPLACE because its app sandbox forbids hard links. Unsupported
// syscalls/filesystems fail; ordinary overwriting rename is never a fallback.
func PublishNoReplace(root *os.Root, stage, destination string) (moved bool, err error) {
	if root == nil {
		return false, errors.New("publish without replacement requires a held directory")
	}
	for _, name := range []string{stage, destination} {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
			return false, errors.New("publish without replacement requires direct child names")
		}
	}
	return publishNoReplace(root, stage, destination)
}
