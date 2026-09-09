//go:build !linux && !darwin

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

func sameDevice(fs.FileInfo, fs.FileInfo) bool { return true }

func fileIdentity(fs.FileInfo) string { return "unsupported" }
