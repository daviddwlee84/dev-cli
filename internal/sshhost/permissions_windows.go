//go:build windows

package sshhost

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

type permissionMetadata struct{ descriptor string }

func permissionValidateAncestor(path string, info fs.FileInfo) error {
	if err := platformRejectReparsePath(path, false); err != nil {
		return err
	}
	return platformValidateHome(path, info)
}

func permissionOpenFile(root *os.Root, name string, _ bool) (*os.File, error) { return root.Open(name) }

func permissionReadMetadata(path string, file *os.File, info fs.FileInfo, target permissionTarget) (permissionMetadata, error) {
	if err := platformRejectReparsePath(path, false); err != nil {
		return permissionMetadata{}, err
	}
	var err error
	if target.directory {
		err = platformValidatePrivateDirectory(path, info)
	} else {
		err = platformValidatePrivateFile(path, file, info)
	}
	if err != nil {
		return permissionMetadata{}, err
	}
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return permissionMetadata{}, err
	}
	return permissionMetadata{descriptor: descriptor.String()}, nil
}

func permissionDesiredMode(snapshot permissionSnapshot) (fs.FileMode, error) {
	return snapshot.info.Mode().Perm(), nil
}
func permissionSameOwner(fs.FileInfo, fs.FileInfo) bool          { return true }
func permissionSameChangeTime(fs.FileInfo, fs.FileInfo) bool     { return true }
func permissionSameNonModeMetadata(a, b permissionMetadata) bool { return a == b }
func permissionChmod(*os.File, fs.FileMode) error                { return ErrManualRemediation }
func permissionSync(*os.File) error                              { return ErrManualRemediation }
