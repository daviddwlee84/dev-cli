//go:build windows

package feedback

import (
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"io/fs"
)

func makePrivateDir(path string) error { return privatefile.MakeDir(path) }
func checkPrivate(path string, info fs.FileInfo, dir bool) error {
	return privatefile.Check(path, info, dir)
}
