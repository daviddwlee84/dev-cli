//go:build windows

package fleet

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const managedWindowsFileAllAccess windows.ACCESS_MASK = 0x001f01ff

type managedWindowsIdentity struct {
	volume uint32
	high   uint32
	low    uint32
}

type managedWindowsSnapshot struct {
	identity managedWindowsIdentity
	digest   [sha256.Size]byte
	content  []byte
}

type managedWindowsRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

var (
	managedWindowsBeforePublish   func(stagedPath, targetPath string)
	managedWindowsAfterBackup     func(backupPath, targetPath string)
	managedWindowsReplaceFileProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")
	managedWindowsReOpenFileProc  = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")
)

func validateManagedPermissions(path string, info fs.FileInfo, expected fs.FileMode) error {
	if err := rejectManagedWindowsReparsePath(path, false); err != nil {
		return err
	}
	directory := info.IsDir()
	if directory != (expected.Perm() == managedFragmentDirectoryMode) {
		return managedWindowsError(path, "has an unexpected file type")
	}
	handle, information, err := openManagedWindowsPath(path, directory, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return managedWindowsError(path, "cannot wrap validated handle")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return managedWindowsError(path, "cannot stat validated handle: %v", err)
	}
	if !os.SameFile(info, openedInfo) {
		return managedWindowsError(path, "changed while opening")
	}
	if !directory && information.NumberOfLinks != 1 {
		return managedWindowsError(path, "has %d hard links, want 1", information.NumberOfLinks)
	}
	return validateManagedWindowsDescriptor(handle, directory)
}

func prepareManagedFragmentDirectory(directory string) error {
	return ensureManagedWindowsDirectory(directory)
}

func writeManagedFragmentOS(request ManagedFragmentWriteRequest) error {
	if len(request.Content) > maxManagedFragmentSize {
		return managedWindowsError(request.Path, "exceeds %d bytes", maxManagedFragmentSize)
	}
	directory := filepath.Dir(request.Path)
	if err := ensureManagedWindowsDirectory(directory); err != nil {
		return err
	}
	directoryHandle, directoryInformation, err := openManagedWindowsPath(
		directory,
		true,
		windows.GENERIC_READ|windows.READ_CONTROL,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(directoryHandle)
	if err := validateManagedWindowsDescriptor(directoryHandle, true); err != nil {
		return err
	}
	directoryIdentity := windowsIdentity(directoryInformation)
	before, err := validateManagedWindowsExpected(request.Path, request.PreviousContent, request.PreviousIdentity, request.Existed)
	if err != nil {
		return err
	}

	staged, stagedPath, err := createManagedWindowsStage(directory)
	if err != nil {
		return err
	}
	stageHandle := windows.Handle(staged.Fd())
	published := false
	defer func() {
		if !published {
			_ = deleteManagedWindowsObject(stageHandle)
		}
		_ = staged.Close()
	}()
	written, err := staged.Write(request.Content)
	if err != nil {
		return err
	}
	if written != len(request.Content) {
		return io.ErrShortWrite
	}
	if err := staged.Sync(); err != nil {
		return err
	}
	stagedSnapshot, err := validateManagedWindowsStage(staged, stagedPath, request.Content)
	if err != nil {
		return fmt.Errorf("validate staged managed fleet fragment: %w", err)
	}

	if managedWindowsBeforePublish != nil {
		managedWindowsBeforePublish(stagedPath, request.Path)
	}
	current, err := validateManagedWindowsExpected(request.Path, request.PreviousContent, request.PreviousIdentity, request.Existed)
	if err != nil {
		return err
	}
	if request.Existed && current.identity != before.identity {
		return managedWindowsError(request.Path, "changed identity before replacement")
	}
	if err := revalidateManagedWindowsDirectory(directory, directoryIdentity); err != nil {
		return err
	}
	finalStage, err := validateManagedWindowsStage(staged, stagedPath, request.Content)
	if err != nil {
		return fmt.Errorf("revalidate staged managed fleet fragment: %w", err)
	}
	if finalStage.identity != stagedSnapshot.identity || finalStage.digest != stagedSnapshot.digest {
		return managedWindowsError(stagedPath, "changed immediately before publication")
	}

	if request.Existed {
		if err := replaceManagedWindowsFragment(
			staged, stagedPath, stagedSnapshot, directoryHandle, directory, directoryIdentity, request, before, &published,
		); err != nil {
			return err
		}
		published = true
		return nil
	}
	renameHandle, err := reopenManagedWindowsHandle(stageHandle, windows.DELETE|windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(renameHandle)
	if err := renameManagedWindowsHandleNoReplace(renameHandle, directoryHandle, filepath.Base(request.Path)); err != nil {
		return managedWindowsError(request.Path, "creation conflicted at publication: %v", err)
	}
	publishedSnapshot, err := readManagedWindowsSnapshot(request.Path)
	if err != nil {
		return fmt.Errorf("validate published managed fleet fragment: %w", err)
	}
	if publishedSnapshot.identity != stagedSnapshot.identity || publishedSnapshot.digest != stagedSnapshot.digest ||
		!bytes.Equal(publishedSnapshot.content, request.Content) {
		return managedWindowsError(request.Path, "published object differs from the validated staging handle")
	}
	if err := revalidateManagedWindowsDirectory(directory, directoryIdentity); err != nil {
		return err
	}
	published = true
	return nil
}

func replaceManagedWindowsFragment(
	staged *os.File,
	stagedPath string,
	stagedSnapshot managedWindowsSnapshot,
	directoryHandle windows.Handle,
	directory string,
	directoryIdentity managedWindowsIdentity,
	request ManagedFragmentWriteRequest,
	before managedWindowsSnapshot,
	published *bool,
) error {
	targetHandle, targetInformation, err := openManagedWindowsPath(
		request.Path,
		false,
		windows.GENERIC_READ|windows.READ_CONTROL,
	)
	if err != nil {
		return err
	}
	target := os.NewFile(uintptr(targetHandle), request.Path)
	if target == nil {
		_ = windows.CloseHandle(targetHandle)
		return managedWindowsError(request.Path, "cannot wrap validated replacement handle")
	}
	targetClosed := false
	defer func() {
		if !targetClosed {
			_ = target.Close()
		}
	}()
	targetSnapshot, err := readManagedWindowsHandleSnapshot(target, request.Path, windowsIdentity(targetInformation))
	if err != nil {
		return err
	}
	if targetSnapshot.identity != before.identity || !bytes.Equal(targetSnapshot.content, request.PreviousContent) {
		return managedWindowsError(request.Path, "changed through the validated target handle")
	}
	byName, err := readManagedWindowsSnapshot(request.Path)
	if err != nil || byName.identity != targetSnapshot.identity || byName.digest != targetSnapshot.digest {
		return errors.Join(managedWindowsError(request.Path, "name no longer identifies the validated target handle"), err)
	}
	if finalStage, err := validateManagedWindowsStage(staged, stagedPath, request.Content); err != nil ||
		finalStage.identity != stagedSnapshot.identity || finalStage.digest != stagedSnapshot.digest {
		if err != nil {
			return err
		}
		return managedWindowsError(stagedPath, "changed immediately before atomic replacement")
	}
	if err := revalidateManagedWindowsDirectory(directory, directoryIdentity); err != nil {
		return err
	}

	backupPath, err := unusedManagedWindowsPath(directory, "backup")
	if err != nil {
		return err
	}
	targetRenameHandle, err := reopenManagedWindowsHandle(targetHandle, windows.DELETE|windows.READ_CONTROL)
	if err != nil {
		return managedWindowsError(request.Path, "cannot reopen validated target for backup: %v", err)
	}
	defer windows.CloseHandle(targetRenameHandle)
	if err := renameManagedWindowsHandleNoReplace(targetRenameHandle, directoryHandle, filepath.Base(backupPath)); err != nil {
		return managedWindowsError(request.Path, "cannot move validated target to backup: %v", err)
	}
	if err := validateManagedWindowsBackup(target, backupPath, targetSnapshot, directory, directoryIdentity); err != nil {
		return err
	}
	if managedWindowsAfterBackup != nil {
		managedWindowsAfterBackup(backupPath, request.Path)
	}
	if _, err := os.Lstat(request.Path); err == nil {
		return managedWindowsError(request.Path, "a concurrent target appeared after backup")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if finalStage, err := validateManagedWindowsStage(staged, stagedPath, request.Content); err != nil ||
		finalStage.identity != stagedSnapshot.identity || finalStage.digest != stagedSnapshot.digest {
		if err != nil {
			return err
		}
		return managedWindowsError(stagedPath, "changed after target backup")
	}
	if err := revalidateManagedWindowsDirectory(directory, directoryIdentity); err != nil {
		return err
	}
	stageRenameHandle, err := reopenManagedWindowsHandle(windows.Handle(staged.Fd()), windows.DELETE|windows.READ_CONTROL)
	if err != nil {
		return managedWindowsError(stagedPath, "cannot reopen validated stage for publication: %v", err)
	}
	defer windows.CloseHandle(stageRenameHandle)
	if err := renameManagedWindowsHandleNoReplace(stageRenameHandle, directoryHandle, filepath.Base(request.Path)); err != nil {
		return managedWindowsError(request.Path, "publication conflicted after backup: %v", err)
	}
	*published = true
	publishedSnapshot, publishedErr := readManagedWindowsSnapshot(request.Path)
	backupSnapshot, backupErr := readManagedWindowsSnapshot(backupPath)
	pathErr := revalidateManagedWindowsDirectory(directory, directoryIdentity)
	if publishedErr != nil || backupErr != nil || pathErr != nil ||
		publishedSnapshot.identity != stagedSnapshot.identity || publishedSnapshot.digest != stagedSnapshot.digest ||
		!bytes.Equal(publishedSnapshot.content, request.Content) ||
		backupSnapshot.identity != targetSnapshot.identity || backupSnapshot.digest != targetSnapshot.digest {
		return errors.Join(managedWindowsError(request.Path, "changed at the guarded replacement boundary"), publishedErr, backupErr, pathErr)
	}
	if err := validateManagedWindowsBackup(target, backupPath, targetSnapshot, directory, directoryIdentity); err != nil {
		return err
	}

	if err := deleteManagedWindowsHandle(targetRenameHandle); err != nil {
		return managedWindowsError(backupPath, "cannot delete validated private backup: %v", err)
	}
	// Delete disposition is the commit point: the new target is fully validated
	// and the old object is being removed through its held handle. A close error
	// cannot safely turn this into rollback without risking deletion of the new
	// target by the caller's staging cleanup.
	_ = target.Close()
	targetClosed = true
	return nil
}

func removeManagedFragmentOS(request ManagedFragmentRemoveRequest) error {
	directory := filepath.Dir(request.Path)
	directoryIdentity, err := managedWindowsPathIdentity(directory, true)
	if err != nil {
		return err
	}
	if err := rejectManagedWindowsReparsePath(request.Path, false); err != nil {
		return err
	}
	handle, information, err := openManagedWindowsPath(
		request.Path,
		false,
		windows.GENERIC_READ|windows.READ_CONTROL|windows.DELETE,
	)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), request.Path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return managedWindowsError(request.Path, "cannot wrap removal handle")
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if information.NumberOfLinks != 1 {
		return managedWindowsError(request.Path, "has %d hard links, want 1", information.NumberOfLinks)
	}
	if request.ExpectedIdentity != nil {
		openedInfo, statErr := file.Stat()
		if statErr != nil {
			return statErr
		}
		if !os.SameFile(openedInfo, request.ExpectedIdentity) {
			return managedWindowsError(request.Path, "changed identity before removal")
		}
	}
	if err := validateManagedWindowsDescriptor(handle, false); err != nil {
		return err
	}
	content, err := io.ReadAll(io.LimitReader(file, maxManagedFragmentSize+1))
	if err != nil {
		return err
	}
	if len(content) > maxManagedFragmentSize || !bytes.Equal(content, request.ExpectedContent) {
		return managedWindowsError(request.Path, "changed before removal")
	}
	current, err := readManagedWindowsSnapshot(request.Path)
	if err != nil {
		return err
	}
	if current.identity != windowsIdentity(information) || !bytes.Equal(current.content, request.ExpectedContent) {
		return managedWindowsError(request.Path, "changed identity before removal")
	}
	if err := revalidateManagedWindowsDirectory(directory, directoryIdentity); err != nil {
		return err
	}
	disposition := struct{ DeleteFile byte }{DeleteFile: 1}
	if err := windows.SetFileInformationByHandle(
		handle,
		windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&disposition)),
		uint32(unsafe.Sizeof(disposition)),
	); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	return nil
}

func ensureManagedWindowsDirectory(directory string) error {
	if err := rejectManagedWindowsReparsePath(directory, true); err != nil {
		return err
	}
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, managedFragmentDirectoryMode); err != nil {
		return err
	}
	if err := rejectManagedWindowsReparsePath(parent, false); err != nil {
		return err
	}
	descriptor, err := managedWindowsProtectedDescriptor(true)
	if err != nil {
		return err
	}
	pointer, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	if err := windows.CreateDirectory(pointer, &attributes); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	return validateManagedPermissions(directory, info, managedFragmentDirectoryMode)
}

func createManagedWindowsStage(directory string) (*os.File, string, error) {
	descriptor, err := managedWindowsProtectedDescriptor(false)
	if err != nil {
		return nil, "", err
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	for attempt := 0; attempt < 32; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		path := filepath.Join(directory, ".dev-fleet-fragment-"+hex.EncodeToString(random[:])+".tmp")
		pointer, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, "", err
		}
		handle, err := windows.CreateFile(
			pointer,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			&attributes,
			windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if err := validateManagedWindowsDescriptor(handle, false); err != nil {
			_ = windows.CloseHandle(handle)
			_ = os.Remove(path)
			return nil, "", err
		}
		return os.NewFile(uintptr(handle), path), path, nil
	}
	return nil, "", errors.New("cannot allocate a unique managed fleet staging file")
}

func validateManagedWindowsStage(file *os.File, path string, expected []byte) (managedWindowsSnapshot, error) {
	handle := windows.Handle(file.Fd())
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return managedWindowsSnapshot{}, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return managedWindowsSnapshot{}, managedWindowsError(path, "staging handle has an unexpected file type")
	}
	if information.NumberOfLinks != 1 {
		return managedWindowsSnapshot{}, managedWindowsError(path, "staging handle has %d hard links, want 1", information.NumberOfLinks)
	}
	if err := validateManagedWindowsDescriptor(handle, false); err != nil {
		return managedWindowsSnapshot{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return managedWindowsSnapshot{}, err
	}
	content, err := io.ReadAll(io.LimitReader(file, maxManagedFragmentSize+1))
	if err != nil {
		return managedWindowsSnapshot{}, err
	}
	if len(content) > maxManagedFragmentSize || !bytes.Equal(content, expected) {
		return managedWindowsSnapshot{}, managedWindowsError(path, "staging handle bytes differ from requested bytes")
	}
	snapshot := managedWindowsSnapshot{
		identity: windowsIdentity(information),
		digest:   sha256.Sum256(content),
		content:  content,
	}
	byName, err := readManagedWindowsSnapshot(path)
	if err != nil {
		return managedWindowsSnapshot{}, managedWindowsError(path, "cannot revalidate staging name: %v", err)
	}
	if byName.identity != snapshot.identity || byName.digest != snapshot.digest || !bytes.Equal(byName.content, content) {
		return managedWindowsSnapshot{}, managedWindowsError(path, "staging name no longer identifies the validated handle")
	}
	return snapshot, nil
}

func readManagedWindowsHandleSnapshot(file *os.File, path string, expected managedWindowsIdentity) (managedWindowsSnapshot, error) {
	handle := windows.Handle(file.Fd())
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return managedWindowsSnapshot{}, err
	}
	identity := windowsIdentity(information)
	if identity != expected {
		return managedWindowsSnapshot{}, managedWindowsError(path, "validated handle changed identity")
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return managedWindowsSnapshot{}, managedWindowsError(path, "validated handle has an unexpected file type")
	}
	if information.NumberOfLinks != 1 {
		return managedWindowsSnapshot{}, managedWindowsError(path, "has %d hard links, want 1", information.NumberOfLinks)
	}
	if err := validateManagedWindowsDescriptor(handle, false); err != nil {
		return managedWindowsSnapshot{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return managedWindowsSnapshot{}, err
	}
	content, err := io.ReadAll(io.LimitReader(file, maxManagedFragmentSize+1))
	if err != nil {
		return managedWindowsSnapshot{}, err
	}
	if len(content) > maxManagedFragmentSize {
		return managedWindowsSnapshot{}, managedWindowsError(path, "exceeds %d bytes", maxManagedFragmentSize)
	}
	return managedWindowsSnapshot{identity: identity, digest: sha256.Sum256(content), content: content}, nil
}

func unusedManagedWindowsPath(directory, kind string) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		path := filepath.Join(directory, ".dev-fleet-fragment-"+kind+"-"+hex.EncodeToString(random[:])+".tmp")
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("cannot allocate a unique private managed fleet path")
}

func replaceManagedWindowsFileAtomic(replaced, replacement, backup string) error {
	replacedPointer, err := windows.UTF16PtrFromString(replaced)
	if err != nil {
		return err
	}
	replacementPointer, err := windows.UTF16PtrFromString(replacement)
	if err != nil {
		return err
	}
	backupPointer, err := windows.UTF16PtrFromString(backup)
	if err != nil {
		return err
	}
	result, _, callErr := managedWindowsReplaceFileProc.Call(
		uintptr(unsafe.Pointer(replacedPointer)),
		uintptr(unsafe.Pointer(replacementPointer)),
		uintptr(unsafe.Pointer(backupPointer)),
		1, // REPLACEFILE_WRITE_THROUGH
		0,
		0,
	)
	if result == 0 {
		if callErr == nil || callErr == syscall.Errno(0) {
			callErr = windows.ERROR_GEN_FAILURE
		}
		return callErr
	}
	return nil
}

func reopenManagedWindowsHandle(original windows.Handle, access uint32) (windows.Handle, error) {
	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	handle, _, callErr := managedWindowsReOpenFileProc.Call(
		uintptr(original), uintptr(access), uintptr(share), 0,
	)
	if windows.Handle(handle) == windows.InvalidHandle {
		if callErr == nil || callErr == syscall.Errno(0) {
			callErr = windows.ERROR_ACCESS_DENIED
		}
		return windows.InvalidHandle, callErr
	}
	return windows.Handle(handle), nil
}

func validateManagedWindowsBackup(
	backup *os.File,
	backupPath string,
	expected managedWindowsSnapshot,
	directory string,
	directoryIdentity managedWindowsIdentity,
) error {
	handleSnapshot, err := readManagedWindowsHandleSnapshot(backup, backupPath, expected.identity)
	if err != nil {
		return err
	}
	byName, err := readManagedWindowsSnapshot(backupPath)
	if err != nil {
		return err
	}
	if handleSnapshot.identity != byName.identity || handleSnapshot.digest != byName.digest ||
		!bytes.Equal(handleSnapshot.content, expected.content) {
		return managedWindowsError(backupPath, "private backup no longer identifies the validated target")
	}
	return revalidateManagedWindowsDirectory(directory, directoryIdentity)
}

func rollbackManagedWindowsReplacement(
	targetPath string,
	backupPath string,
	staged *os.File,
	stagedSnapshot managedWindowsSnapshot,
	backupSnapshot managedWindowsSnapshot,
	directory string,
	directoryIdentity managedWindowsIdentity,
) error {
	currentTarget, err := readManagedWindowsSnapshot(targetPath)
	if err != nil || currentTarget.identity != stagedSnapshot.identity || currentTarget.digest != stagedSnapshot.digest {
		return errors.Join(managedWindowsError(targetPath, "cannot roll back a target no longer owned by the staging handle"), err)
	}
	currentBackup, err := readManagedWindowsSnapshot(backupPath)
	if err != nil || currentBackup.identity != backupSnapshot.identity || currentBackup.digest != backupSnapshot.digest {
		return errors.Join(managedWindowsError(backupPath, "cannot roll back a changed private backup"), err)
	}
	failedPath, err := unusedManagedWindowsPath(directory, "failed-publication")
	if err != nil {
		return err
	}
	if err := replaceManagedWindowsFileAtomic(targetPath, backupPath, failedPath); err != nil {
		return managedWindowsError(targetPath, "atomic rollback failed: %v", err)
	}
	restored, restoredErr := readManagedWindowsSnapshot(targetPath)
	failed, failedErr := readManagedWindowsSnapshot(failedPath)
	pathErr := revalidateManagedWindowsDirectory(directory, directoryIdentity)
	if restoredErr != nil || failedErr != nil || pathErr != nil ||
		restored.identity != backupSnapshot.identity || restored.digest != backupSnapshot.digest ||
		failed.identity != stagedSnapshot.identity || failed.digest != stagedSnapshot.digest {
		return errors.Join(managedWindowsError(targetPath, "atomic rollback identities differ"), restoredErr, failedErr, pathErr)
	}
	// staged still refers to failedPath, so the caller's held-handle cleanup can
	// remove only that failed publication without touching a concurrent object.
	_ = staged
	return nil
}

func renameManagedWindowsHandleNoReplace(handle, directory windows.Handle, name string) error {
	encodedName, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	fileNameLength := (len(encodedName) - 1) * 2
	var layout managedWindowsRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+fileNameLength)
	information := (*managedWindowsRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.RootDirectory = directory
	information.FileNameLength = uint32(fileNameLength)
	fileName := unsafe.Slice(&information.FileName[0], len(encodedName)-1)
	copy(fileName, encodedName[:len(encodedName)-1])
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(
		handle,
		&status,
		&buffer[0],
		uint32(len(buffer)),
		windows.FileRenameInformation,
	)
}

func deleteManagedWindowsHandle(handle windows.Handle) error {
	disposition := struct{ DeleteFile byte }{DeleteFile: 1}
	return windows.SetFileInformationByHandle(
		handle,
		windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&disposition)),
		uint32(unsafe.Sizeof(disposition)),
	)
}

func deleteManagedWindowsObject(handle windows.Handle) error {
	deleteHandle, err := reopenManagedWindowsHandle(handle, windows.DELETE|windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(deleteHandle)
	return deleteManagedWindowsHandle(deleteHandle)
}

func validateManagedWindowsExpected(path string, expected []byte, expectedIdentity fs.FileInfo, existed bool) (managedWindowsSnapshot, error) {
	if !existed {
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return managedWindowsSnapshot{}, nil
		} else if err != nil {
			return managedWindowsSnapshot{}, err
		}
		return managedWindowsSnapshot{}, managedWindowsError(path, "appeared before creation")
	}
	if expectedIdentity != nil {
		info, err := os.Lstat(path)
		if err != nil {
			return managedWindowsSnapshot{}, err
		}
		if !os.SameFile(info, expectedIdentity) {
			return managedWindowsSnapshot{}, managedWindowsError(path, "changed expected identity")
		}
	}
	snapshot, err := readManagedWindowsSnapshot(path)
	if err != nil {
		return managedWindowsSnapshot{}, managedWindowsError(path, "cannot revalidate expected file: %v", err)
	}
	if !bytes.Equal(snapshot.content, expected) {
		return managedWindowsSnapshot{}, managedWindowsError(path, "changed before replacement")
	}
	return snapshot, nil
}

func readManagedWindowsSnapshot(path string) (managedWindowsSnapshot, error) {
	if err := rejectManagedWindowsReparsePath(path, false); err != nil {
		return managedWindowsSnapshot{}, err
	}
	handle, information, err := openManagedWindowsPath(path, false, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return managedWindowsSnapshot{}, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return managedWindowsSnapshot{}, managedWindowsError(path, "cannot wrap read handle")
	}
	defer file.Close()
	if information.NumberOfLinks != 1 {
		return managedWindowsSnapshot{}, managedWindowsError(path, "has %d hard links, want 1", information.NumberOfLinks)
	}
	if err := validateManagedWindowsDescriptor(handle, false); err != nil {
		return managedWindowsSnapshot{}, err
	}
	content, err := io.ReadAll(io.LimitReader(file, maxManagedFragmentSize+1))
	if err != nil {
		return managedWindowsSnapshot{}, err
	}
	if len(content) > maxManagedFragmentSize {
		return managedWindowsSnapshot{}, managedWindowsError(path, "exceeds %d bytes", maxManagedFragmentSize)
	}
	current, err := managedWindowsPathIdentity(path, false)
	if err != nil {
		return managedWindowsSnapshot{}, err
	}
	identity := windowsIdentity(information)
	if current != identity {
		return managedWindowsSnapshot{}, managedWindowsError(path, "changed while reading")
	}
	return managedWindowsSnapshot{identity: identity, digest: sha256.Sum256(content), content: content}, nil
}

func revalidateManagedWindowsDirectory(path string, expected managedWindowsIdentity) error {
	current, err := managedWindowsPathIdentity(path, true)
	if err != nil {
		return err
	}
	if current != expected {
		return managedWindowsError(path, "changed identity during operation")
	}
	return nil
}

func managedWindowsPathIdentity(path string, directory bool) (managedWindowsIdentity, error) {
	if err := rejectManagedWindowsReparsePath(path, false); err != nil {
		return managedWindowsIdentity{}, err
	}
	handle, information, err := openManagedWindowsPath(path, directory, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return managedWindowsIdentity{}, err
	}
	defer windows.CloseHandle(handle)
	if err := validateManagedWindowsDescriptor(handle, directory); err != nil {
		return managedWindowsIdentity{}, err
	}
	return windowsIdentity(information), nil
}

func openManagedWindowsPath(path string, directory bool, access uint32) (windows.Handle, windows.ByHandleFileInformation, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, err
	}
	flags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(
		pointer,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, err
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, managedWindowsError(path, "is a reparse point")
	}
	isDirectory := information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != directory {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, managedWindowsError(path, "has an unexpected file type")
	}
	return handle, information, nil
}

func rejectManagedWindowsReparsePath(path string, allowMissingFinal bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absolute = filepath.Clean(absolute)
	volume := filepath.VolumeName(absolute)
	if volume == "" {
		return managedWindowsError(path, "has no volume")
	}
	rest := strings.TrimPrefix(absolute, volume)
	current := volume
	if strings.HasPrefix(rest, `\`) || strings.HasPrefix(rest, `/`) {
		current += string(filepath.Separator)
		rest = strings.TrimLeft(rest, `\/`)
	}
	components := strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' })
	for index, component := range components {
		current = filepath.Join(current, component)
		pointer, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(pointer)
		if err != nil {
			missing := errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
			if allowMissingFinal && missing {
				return nil
			}
			return err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return managedWindowsError(current, "is a reparse point at component %d", index)
		}
	}
	return nil
}

func managedWindowsProtectedDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := managedWindowsCurrentUserSID()
	if err != nil {
		return nil, err
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sddl := fmt.Sprintf("O:%sD:P(A;%s;FA;;;SY)(A;%s;FA;;;%s)", user.String(), inheritance, inheritance, user.String())
	return windows.SecurityDescriptorFromString(sddl)
}

func validateManagedWindowsDescriptor(handle windows.Handle, directory bool) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	if descriptor == nil {
		return managedWindowsError("managed fleet path", "has no security descriptor")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return managedWindowsError("managed fleet path", "inherits its DACL")
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	user, err := managedWindowsCurrentUserSID()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user) {
		return managedWindowsError("managed fleet path", "is not owned by the current user")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		if err != nil {
			return err
		}
		return managedWindowsError("managed fleet path", "has no DACL")
	}
	if dacl.AceCount != 2 {
		return managedWindowsError("managed fleet path", "DACL has %d entries, want current-user and SYSTEM only", dacl.AceCount)
	}
	wantFlags := uint8(0)
	if directory {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	seenUser, seenSystem := false, false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != wantFlags || ace.Mask != managedWindowsFileAllAccess {
			return managedWindowsError("managed fleet path", "DACL entry %d is not an explicit full-control entry", index)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.Equals(user):
			seenUser = true
		case sid.Equals(system):
			seenSystem = true
		default:
			return managedWindowsError("managed fleet path", "DACL grants an unexpected SID")
		}
	}
	if !seenUser || !seenSystem {
		return managedWindowsError("managed fleet path", "DACL does not grant current-user and SYSTEM")
	}
	return nil
}

func managedWindowsCurrentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func windowsIdentity(information windows.ByHandleFileInformation) managedWindowsIdentity {
	return managedWindowsIdentity{
		volume: information.VolumeSerialNumber,
		high:   information.FileIndexHigh,
		low:    information.FileIndexLow,
	}
}

func managedWindowsError(path, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	return fmt.Errorf("managed fleet path %s %s: %w", path, message, ErrManagedFragmentConflict)
}
