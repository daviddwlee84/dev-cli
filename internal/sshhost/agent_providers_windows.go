//go:build windows

package sshhost

import (
	"fmt"
	"io/fs"
)

func platformAgentDirectory(path string, info fs.FileInfo) error {
	return platformValidateHome(path, info)
}

// Filesystem stat cannot attest a named pipe's server identity or ACL. Explicit
// agent selection fails closed; ordinary OpenSSH retains its native agent path.
func platformAgentSocket(string, fs.FileInfo) error {
	return fmt.Errorf("explicit Windows SSH agent pipes cannot yet be attested: %w", ErrManualRemediation)
}
