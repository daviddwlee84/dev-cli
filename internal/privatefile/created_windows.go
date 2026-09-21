//go:build windows

package privatefile

import (
	"golang.org/x/sys/windows"
	"os"
)

// ProtectCreatedFile binds native owner/DACL protection to the retained creation
// handle before any sensitive bytes are written. Opening a security handle by
// name must not accidentally protect a different file substituted at that name.
func ProtectCreatedFile(file *os.File) error {
	var expected windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &expected); err != nil {
		return err
	}
	return protectCreated(file.Name(), 0o600, &expected)
}
