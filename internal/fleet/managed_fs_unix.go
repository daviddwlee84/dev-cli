//go:build unix

package fleet

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

var (
	managedUnixBeforeFinalValidation func(directory, stagedPath, targetPath string)
	managedUnixBeforePublish         func(directory, stagedPath, targetPath string)
	managedUnixAfterRenameOut        func(directory, backupPath, targetPath string)
)

type managedUnixDirectory struct {
	path     string
	root     *os.Root
	syncFile *os.File
	identity fs.FileInfo
}

type managedUnixSnapshot struct {
	content []byte
	info    fs.FileInfo
}

func validateManagedPermissions(path string, info fs.FileInfo, expected fs.FileMode) error {
	if info.Mode().Perm() != expected.Perm() {
		return fmt.Errorf("managed fleet path %s has mode %04o; want %04o: %w", path, info.Mode().Perm(), expected.Perm(), ErrManagedFragmentConflict)
	}
	return nil
}

func prepareManagedFragmentDirectory(directory string) error {
	if err := os.MkdirAll(directory, managedFragmentDirectoryMode); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	return validateManagedDirectoryInfo(directory, info)
}

func writeManagedFragmentOS(request ManagedFragmentWriteRequest) error {
	if len(request.Content) > maxManagedFragmentSize {
		return fmt.Errorf("managed fleet fragment %s exceeds %d bytes: %w", request.Path, maxManagedFragmentSize, ErrManagedFragmentConflict)
	}
	directoryPath := filepath.Dir(request.Path)
	if err := prepareManagedFragmentDirectory(directoryPath); err != nil {
		return err
	}
	directory, err := openManagedUnixDirectory(directoryPath)
	if err != nil {
		return err
	}
	defer directory.close()

	targetName := filepath.Base(request.Path)
	initial, err := directory.validateExpected(targetName, request.PreviousContent, request.Existed)
	if err != nil {
		return err
	}
	if request.Existed && request.PreviousIdentity != nil && !os.SameFile(initial.info, request.PreviousIdentity) {
		return managedUnixConflict(request.Path, "changed identity before staging")
	}
	stagedName, staged, stagedSnapshot, err := directory.createStage(request.Content)
	if err != nil {
		return err
	}
	defer staged.Close()
	defer directory.removeIfSame(stagedName, stagedSnapshot.info)

	stagedPath := filepath.Join(directoryPath, stagedName)
	if managedUnixBeforeFinalValidation != nil {
		managedUnixBeforeFinalValidation(directoryPath, stagedPath, request.Path)
	}
	if err := directory.revalidate(); err != nil {
		return err
	}
	current, err := directory.validateExpected(targetName, request.PreviousContent, request.Existed)
	if err != nil {
		return err
	}
	if request.Existed && !os.SameFile(initial.info, current.info) {
		return managedUnixConflict(request.Path, "changed identity before publication")
	}
	if err := directory.validateStage(stagedName, staged, stagedSnapshot, request.Content); err != nil {
		return err
	}

	if managedUnixBeforePublish != nil {
		managedUnixBeforePublish(directoryPath, stagedPath, request.Path)
	}
	if request.Existed {
		return directory.replace(stagedName, targetName, stagedSnapshot, initial, request.Content)
	}
	return directory.create(stagedName, targetName, stagedSnapshot, request.Content)
}

func removeManagedFragmentOS(request ManagedFragmentRemoveRequest) error {
	directoryPath := filepath.Dir(request.Path)
	directory, err := openManagedUnixDirectory(directoryPath)
	if err != nil {
		return err
	}
	defer directory.close()

	targetName := filepath.Base(request.Path)
	initial, err := directory.validateExpected(targetName, request.ExpectedContent, true)
	if err != nil {
		return err
	}
	if request.ExpectedIdentity != nil && !os.SameFile(initial.info, request.ExpectedIdentity) {
		return managedUnixConflict(request.Path, "changed identity before removal staging")
	}
	backupName, err := directory.allocateBackupName()
	if err != nil {
		return err
	}
	backupPath := filepath.Join(directoryPath, backupName)

	if managedUnixBeforeFinalValidation != nil {
		managedUnixBeforeFinalValidation(directoryPath, backupPath, request.Path)
	}
	if err := directory.revalidate(); err != nil {
		return err
	}
	current, err := directory.validateExpected(targetName, request.ExpectedContent, true)
	if err != nil {
		return err
	}
	if !os.SameFile(initial.info, current.info) {
		return managedUnixConflict(request.Path, "changed identity before removal")
	}

	if managedUnixBeforePublish != nil {
		managedUnixBeforePublish(directoryPath, backupPath, request.Path)
	}
	if err := renameManagedUnixNoReplace(directory.syncFile, targetName, backupName); err != nil {
		return managedUnixConflict(request.Path, "cannot atomically rename out for removal: %v", err)
	}
	if err := directory.sync(); err != nil {
		return errors.Join(err, directory.restoreRenamedOut(backupName, targetName, initial))
	}
	if managedUnixAfterRenameOut != nil {
		managedUnixAfterRenameOut(directoryPath, backupPath, request.Path)
	}

	moved, movedErr := directory.read(backupName)
	pathErr := directory.revalidate()
	if movedErr != nil || pathErr != nil ||
		!os.SameFile(moved.info, initial.info) || !bytes.Equal(moved.content, request.ExpectedContent) {
		rollbackErr := managedUnixConflict(request.Path, "cannot establish the moved object for rollback")
		if movedErr == nil {
			rollbackErr = directory.restoreRenamedOut(backupName, targetName, moved)
		}
		return errors.Join(
			managedUnixConflict(request.Path, "changed at the removal boundary"),
			movedErr,
			pathErr,
			rollbackErr,
		)
	}
	if err := directory.removeSnapshot(backupName, moved); err != nil {
		return errors.Join(err, directory.restoreRenamedOut(backupName, targetName, moved))
	}
	if err := directory.revalidate(); err != nil {
		return err
	}
	return directory.sync()
}

func openManagedUnixDirectory(path string) (*managedUnixDirectory, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect managed fleet directory %s: %w", path, err)
	}
	if err := validateManagedDirectoryInfo(path, before); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open managed fleet directory %s: %w", path, err)
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("stat rooted managed fleet directory %s: %w", path, err)
	}
	if !os.SameFile(before, rootInfo) {
		_ = root.Close()
		return nil, managedUnixConflict(path, "changed while opening")
	}
	syncFile, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open managed fleet directory descriptor %s: %w", path, err)
	}
	syncInfo, err := syncFile.Stat()
	if err != nil || !os.SameFile(before, syncInfo) {
		_ = syncFile.Close()
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("stat managed fleet directory descriptor %s: %w", path, err)
		}
		return nil, managedUnixConflict(path, "changed while opening its descriptor")
	}
	return &managedUnixDirectory{path: path, root: root, syncFile: syncFile, identity: before}, nil
}

func (directory *managedUnixDirectory) close() {
	_ = directory.syncFile.Close()
	_ = directory.root.Close()
}

func (directory *managedUnixDirectory) revalidate() error {
	current, err := os.Lstat(directory.path)
	if err != nil {
		return fmt.Errorf("revalidate managed fleet directory %s: %w", directory.path, err)
	}
	if err := validateManagedDirectoryInfo(directory.path, current); err != nil {
		return err
	}
	rooted, err := directory.root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(directory.identity, current) || !os.SameFile(directory.identity, rooted) {
		return managedUnixConflict(directory.path, "changed identity during operation")
	}
	return nil
}

func (directory *managedUnixDirectory) read(name string) (managedUnixSnapshot, error) {
	path := filepath.Join(directory.path, name)
	before, err := directory.root.Lstat(name)
	if err != nil {
		return managedUnixSnapshot{}, fmt.Errorf("inspect managed fleet fragment %s: %w", path, err)
	}
	if err := validateManagedUnixFileInfo(path, before, managedFragmentFileMode); err != nil {
		return managedUnixSnapshot{}, err
	}
	opened, err := directory.root.Open(name)
	if err != nil {
		return managedUnixSnapshot{}, fmt.Errorf("open managed fleet fragment %s: %w", path, err)
	}
	defer opened.Close()
	openedInfo, err := opened.Stat()
	if err != nil {
		return managedUnixSnapshot{}, err
	}
	if !os.SameFile(before, openedInfo) {
		return managedUnixSnapshot{}, managedUnixConflict(path, "changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(opened, maxManagedFragmentSize+1))
	if err != nil {
		return managedUnixSnapshot{}, err
	}
	if len(content) > maxManagedFragmentSize {
		return managedUnixSnapshot{}, managedUnixConflict(path, "exceeds %d bytes", maxManagedFragmentSize)
	}
	after, err := directory.root.Lstat(name)
	if err != nil {
		return managedUnixSnapshot{}, fmt.Errorf("reinspect managed fleet fragment %s: %w", path, err)
	}
	if err := validateManagedUnixFileInfo(path, after, managedFragmentFileMode); err != nil {
		return managedUnixSnapshot{}, err
	}
	if !os.SameFile(openedInfo, after) {
		return managedUnixSnapshot{}, managedUnixConflict(path, "changed while reading")
	}
	return managedUnixSnapshot{content: content, info: after}, nil
}

func (directory *managedUnixDirectory) validateExpected(name string, expected []byte, existed bool) (managedUnixSnapshot, error) {
	if !existed {
		if _, err := directory.root.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			return managedUnixSnapshot{}, nil
		} else if err != nil {
			return managedUnixSnapshot{}, err
		}
		return managedUnixSnapshot{}, managedUnixConflict(filepath.Join(directory.path, name), "appeared before creation")
	}
	snapshot, err := directory.read(name)
	if err != nil {
		return managedUnixSnapshot{}, managedUnixConflict(filepath.Join(directory.path, name), "cannot revalidate expected file: %v", err)
	}
	if !bytes.Equal(snapshot.content, expected) {
		return managedUnixSnapshot{}, managedUnixConflict(filepath.Join(directory.path, name), "changed before mutation")
	}
	return snapshot, nil
}

func (directory *managedUnixDirectory) allocateBackupName() (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := ".dev-fleet-fragment-backup-" + hex.EncodeToString(random[:]) + ".tmp"
		if _, err := directory.root.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			return name, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("cannot allocate a unique managed fleet backup name")
}

func (directory *managedUnixDirectory) createStage(content []byte) (string, *os.File, managedUnixSnapshot, error) {
	for attempt := 0; attempt < 32; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, managedUnixSnapshot{}, err
		}
		name := ".dev-fleet-fragment-" + hex.EncodeToString(random[:]) + ".tmp"
		file, err := directory.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, managedFragmentFileMode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, managedUnixSnapshot{}, err
		}
		if err := file.Chmod(managedFragmentFileMode); err != nil {
			_ = file.Close()
			_ = directory.root.Remove(name)
			return "", nil, managedUnixSnapshot{}, err
		}
		if len(content) > 0 {
			written, err := file.Write(content)
			if err != nil || written != len(content) {
				_ = file.Close()
				_ = directory.root.Remove(name)
				if err != nil {
					return "", nil, managedUnixSnapshot{}, err
				}
				return "", nil, managedUnixSnapshot{}, io.ErrShortWrite
			}
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = directory.root.Remove(name)
			return "", nil, managedUnixSnapshot{}, err
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			_ = directory.root.Remove(name)
			return "", nil, managedUnixSnapshot{}, err
		}
		snapshot := managedUnixSnapshot{content: bytes.Clone(content), info: info}
		if err := directory.validateStage(name, file, snapshot, content); err != nil {
			_ = file.Close()
			_ = directory.removeIfSame(name, info)
			return "", nil, managedUnixSnapshot{}, err
		}
		return name, file, snapshot, nil
	}
	return "", nil, managedUnixSnapshot{}, errors.New("cannot allocate a unique managed fleet staging file")
}

func (directory *managedUnixDirectory) validateStage(name string, file *os.File, expected managedUnixSnapshot, content []byte) error {
	path := filepath.Join(directory.path, name)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := validateManagedUnixFileInfo(path, info, managedFragmentFileMode); err != nil {
		return err
	}
	if !os.SameFile(expected.info, info) {
		return managedUnixConflict(path, "staging handle changed identity")
	}
	read, err := io.ReadAll(io.LimitReader(file, maxManagedFragmentSize+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(read, content) {
		return managedUnixConflict(path, "staging handle bytes changed")
	}
	byName, err := directory.read(name)
	if err != nil {
		return managedUnixConflict(path, "cannot revalidate staging name: %v", err)
	}
	if !os.SameFile(info, byName.info) || !bytes.Equal(byName.content, content) {
		return managedUnixConflict(path, "staging name no longer identifies the validated file")
	}
	return nil
}

func (directory *managedUnixDirectory) create(stagedName, targetName string, staged managedUnixSnapshot, desired []byte) error {
	if err := renameManagedUnixNoReplace(directory.syncFile, stagedName, targetName); err != nil {
		return managedUnixConflict(filepath.Join(directory.path, targetName), "appeared at the publication boundary: %v", err)
	}
	published, readErr := directory.read(targetName)
	pathErr := directory.revalidate()
	if readErr != nil || pathErr != nil || !os.SameFile(staged.info, published.info) || !bytes.Equal(published.content, desired) {
		rollbackErr := renameManagedUnixNoReplace(directory.syncFile, targetName, stagedName)
		if rollbackErr == nil {
			rollbackErr = directory.sync()
		}
		return errors.Join(managedUnixConflict(filepath.Join(directory.path, targetName), "changed at the creation boundary"), readErr, pathErr, rollbackErr)
	}
	final, err := directory.read(targetName)
	if err != nil || !os.SameFile(staged.info, final.info) || !bytes.Equal(final.content, desired) {
		return errors.Join(managedUnixConflict(filepath.Join(directory.path, targetName), "changed after creation"), err)
	}
	return directory.sync()
}

func (directory *managedUnixDirectory) replace(stagedName, targetName string, staged, previous managedUnixSnapshot, desired []byte) error {
	if err := exchangeManagedUnixFiles(directory.syncFile, stagedName, targetName); err != nil {
		return managedUnixConflict(filepath.Join(directory.path, targetName), "cannot atomically replace: %v", err)
	}
	published, publishedErr := directory.read(targetName)
	displaced, displacedErr := directory.read(stagedName)
	pathErr := directory.revalidate()
	if publishedErr != nil || displacedErr != nil || pathErr != nil ||
		!os.SameFile(published.info, staged.info) || !bytes.Equal(published.content, desired) ||
		!os.SameFile(displaced.info, previous.info) || !bytes.Equal(displaced.content, previous.content) {
		conflict := managedUnixConflict(filepath.Join(directory.path, targetName), "changed at the replacement boundary")
		return errors.Join(conflict, publishedErr, displacedErr, pathErr, directory.rollbackExchange(stagedName, targetName))
	}
	if err := directory.removeSnapshot(stagedName, displaced); err != nil {
		return errors.Join(err, directory.rollbackExchange(stagedName, targetName))
	}
	if err := directory.revalidate(); err != nil {
		return err
	}
	final, err := directory.read(targetName)
	if err != nil || !os.SameFile(final.info, staged.info) || !bytes.Equal(final.content, desired) {
		return errors.Join(managedUnixConflict(filepath.Join(directory.path, targetName), "changed after replacement"), err)
	}
	return directory.sync()
}

func (directory *managedUnixDirectory) rollbackExchange(stagedName, targetName string) error {
	if err := exchangeManagedUnixFiles(directory.syncFile, stagedName, targetName); err != nil {
		return fmt.Errorf("rollback managed fleet exchange: %w", err)
	}
	return directory.sync()
}

func (directory *managedUnixDirectory) restoreRenamedOut(backupName, targetName string, expected managedUnixSnapshot) error {
	current, err := directory.read(backupName)
	if err != nil {
		return managedUnixConflict(filepath.Join(directory.path, targetName), "rollback cannot revalidate renamed-out fragment: %v", err)
	}
	if !os.SameFile(current.info, expected.info) || !bytes.Equal(current.content, expected.content) {
		return managedUnixConflict(filepath.Join(directory.path, targetName), "rollback found a changed renamed-out fragment")
	}
	if err := renameManagedUnixNoReplace(directory.syncFile, backupName, targetName); err != nil {
		return managedUnixConflict(filepath.Join(directory.path, targetName), "rollback requires a vacant canonical name: %v", err)
	}
	return directory.sync()
}

func (directory *managedUnixDirectory) removeSnapshot(name string, expected managedUnixSnapshot) error {
	current, err := directory.read(name)
	if err != nil {
		return err
	}
	if !os.SameFile(current.info, expected.info) || !bytes.Equal(current.content, expected.content) {
		return managedUnixConflict(filepath.Join(directory.path, name), "changed immediately before removal")
	}
	if err := directory.root.Remove(name); err != nil {
		return err
	}
	return nil
}

func (directory *managedUnixDirectory) removeIfSame(name string, expected fs.FileInfo) error {
	if expected == nil {
		return nil
	}
	current, err := directory.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(current, expected) {
		return nil
	}
	return directory.root.Remove(name)
}

func (directory *managedUnixDirectory) sync() error {
	return directory.syncFile.Sync()
}

func validateManagedUnixFileInfo(path string, info fs.FileInfo, mode fs.FileMode) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return managedUnixConflict(path, "is a symlink")
	}
	if !info.Mode().IsRegular() {
		return managedUnixConflict(path, "is not a regular file")
	}
	return validateManagedPermissions(path, info, mode)
}

func managedUnixConflict(path, format string, args ...any) error {
	return fmt.Errorf("managed fleet path %s %s: %w", path, fmt.Sprintf(format, args...), ErrManagedFragmentConflict)
}
