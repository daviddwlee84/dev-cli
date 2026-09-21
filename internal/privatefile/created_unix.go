//go:build !windows

package privatefile

import "os"

// ProtectCreatedFile protects an exclusively created, still-empty temporary file
// before sensitive bytes are written. The caller retains the open creation handle.
func ProtectCreatedFile(file *os.File) error { return file.Chmod(0o600) }
