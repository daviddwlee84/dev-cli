//go:build windows

package prflow

import (
	"errors"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"golang.org/x/sys/windows"
	"io/fs"
)

func checkMergeStateOwner(path string, _ fs.FileInfo) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return errors.New("application state owner unavailable")
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	creator, err := privatefile.CreatorOwner()
	if err != nil {
		return err
	}
	if !owner.Equals(current.User.Sid) && !owner.Equals(creator) {
		return errors.New("application state directory has a foreign owner")
	}
	return nil
}
