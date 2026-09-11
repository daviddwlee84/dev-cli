//go:build !linux && !darwin && !windows

package configedit

import (
	"errors"
	"io/fs"
	"os"
)

type Metadata struct{}

func checkDirectory(string, fs.FileInfo) error              { return nil }
func captureMetadata(string, fs.FileInfo) (Metadata, error) { return Metadata{}, nil }
func (Metadata) prepare(*os.File) error {
	return errors.New("configuration writes require a verified macOS/Linux security backend")
}

func checkAncestor(string, fs.FileInfo) error { return nil }

func sameDevice(string, string, fs.FileInfo, fs.FileInfo) bool { return true }

func fileIdentity(string, fs.FileInfo) string { return "unsupported" }

func makeDirectory(path string) error                     { return os.Mkdir(path, 0o700) }
func privateRecoveryMode(_ string, info fs.FileInfo) bool { return info.Mode().Perm() == 0o700 }
