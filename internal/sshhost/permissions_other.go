//go:build !darwin && !linux && !windows

package sshhost

import (
	"io/fs"
	"os"
)

type permissionMetadata struct{}

func permissionValidateAncestor(string, fs.FileInfo) error        { return ErrManualRemediation }
func permissionOpenFile(*os.Root, string, bool) (*os.File, error) { return nil, ErrManualRemediation }
func permissionReadMetadata(string, *os.File, fs.FileInfo, permissionTarget) (permissionMetadata, error) {
	return permissionMetadata{}, ErrManualRemediation
}
func permissionDesiredMode(permissionSnapshot) (fs.FileMode, error)             { return 0, ErrManualRemediation }
func permissionSameOwner(fs.FileInfo, fs.FileInfo) bool                         { return false }
func permissionSameChangeTime(fs.FileInfo, fs.FileInfo) bool                    { return false }
func permissionSameNonModeMetadata(permissionMetadata, permissionMetadata) bool { return false }
func permissionChmod(*os.File, fs.FileMode) error                               { return ErrManualRemediation }
func permissionSync(*os.File) error                                             { return ErrManualRemediation }
