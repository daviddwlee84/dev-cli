//go:build unix

package sshhost

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
	"testing"
)

type securityKeyMetadataFixture struct {
	fs.FileInfo
	mode fs.FileMode
	uid  uint32
}

func (f securityKeyMetadataFixture) Mode() fs.FileMode { return f.mode }
func (f securityKeyMetadataFixture) Sys() any          { return &syscall.Stat_t{Uid: f.uid} }

func TestSecurityKeyRegularFilesNeverUseStickyDirectoryException(t *testing.T) {
	path := t.TempDir()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, executable := range []bool{false, true} {
		unsafe := securityKeyMetadataFixture{FileInfo: info, mode: os.ModeSticky | 0o777, uid: 0}
		if err := validateSecurityKeyFileMetadata(path, unsafe, executable); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("sticky writable regular file accepted (executable=%v): %v", executable, err)
		}
		safe := securityKeyMetadataFixture{FileInfo: info, mode: 0o755, uid: 0}
		if err := validateSecurityKeyFileMetadata(path, safe, executable); err != nil {
			t.Fatalf("read-only root executable/library rejected: %v", err)
		}
	}
	directory := securityKeyMetadataFixture{FileInfo: info, mode: os.ModeDir | os.ModeSticky | 0o777, uid: 0}
	if err := platformAgentDirectory(path, directory); err != nil {
		t.Fatalf("actual sticky directory exception changed: %v", err)
	}
}
