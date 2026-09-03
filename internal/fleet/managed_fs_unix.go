//go:build !windows

package fleet

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func validateManagedPermissions(path string, info fs.FileInfo, expected fs.FileMode) error {
	if info.Mode().Perm() != expected.Perm() {
		return fmt.Errorf("managed fleet path %s has mode %04o; want %04o: %w", path, info.Mode().Perm(), expected.Perm(), ErrManagedFragmentConflict)
	}
	return nil
}

func writeManagedFragmentOS(request ManagedFragmentWriteRequest) error {
	directory := filepath.Dir(request.Path)
	if err := os.MkdirAll(directory, request.DirectoryMode); err != nil {
		return err
	}
	if err := os.Chmod(directory, request.DirectoryMode); err != nil {
		return err
	}
	if err := validateManagedDirectory(directory); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(directory, ".dev-fleet-fragment-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(request.FileMode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(request.Content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := verifyExpectedManagedContent(request.Path, request.PreviousContent, request.Existed); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, request.Path); err != nil {
		return err
	}
	return syncManagedDirectory(directory)
}

func removeManagedFragmentOS(request ManagedFragmentRemoveRequest) error {
	content, _, err := readStableManagedFile(request.Path)
	if err != nil {
		return err
	}
	if !bytes.Equal(content, request.ExpectedContent) {
		return fmt.Errorf("managed fleet fragment %s changed before removal: %w", request.Path, ErrManagedFragmentConflict)
	}
	if err := os.Remove(request.Path); err != nil {
		return err
	}
	return syncManagedDirectory(filepath.Dir(request.Path))
}

func verifyExpectedManagedContent(path string, expected []byte, existed bool) error {
	if !existed {
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return fmt.Errorf("managed fleet fragment %s appeared before creation: %w", path, ErrManagedFragmentConflict)
	}
	content, _, err := readStableManagedFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(content, expected) {
		return fmt.Errorf("managed fleet fragment %s changed before replacement: %w", path, ErrManagedFragmentConflict)
	}
	return nil
}

func syncManagedDirectory(directory string) error {
	opened, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer opened.Close()
	return opened.Sync()
}
