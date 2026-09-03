//go:build windows

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

	"golang.org/x/sys/windows"
)

func checkPrivateConfigFile(path string) error {
	handle, information, err := openManagedWindowsPath(
		path,
		false,
		windows.GENERIC_READ|windows.READ_CONTROL,
	)
	if err != nil {
		return fmt.Errorf("%s contains a plaintext SSH password and must have a protected current-user-and-SYSTEM DACL: %w", path, err)
	}
	defer windows.CloseHandle(handle)
	if information.NumberOfLinks != 1 {
		return fmt.Errorf("%s contains a plaintext SSH password and has %d hard links, want 1", path, information.NumberOfLinks)
	}
	if err := validateManagedWindowsDescriptor(handle, false); err != nil {
		return fmt.Errorf("%s contains a plaintext SSH password and must have a protected current-user-and-SYSTEM DACL: %w", path, err)
	}
	return nil
}

var privateConfigWindowsBeforePublish func(path string)

// WritePrivateConfigFile creates or explicitly overwrites a primary fleet
// config from a protected same-directory stage. Replacement keeps the old
// object in an atomic backup until post-publication identity checks succeed.
func WritePrivateConfigFile(path string, content []byte, overwrite bool) error {
	if err := rejectManagedWindowsReparsePath(filepath.Dir(path), false); err != nil {
		return err
	}
	directoryHandle, directoryInformation, err := openManagedWindowsPath(
		filepath.Dir(path), true, windows.GENERIC_READ|windows.READ_CONTROL,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(directoryHandle)
	directoryIdentity := windowsIdentity(directoryInformation)

	before, exists, target, err := openPrivateConfigWindowsTarget(path)
	if err != nil {
		return err
	}
	if target != nil {
		defer target.Close()
	}
	if exists && !overwrite {
		return fs.ErrExist
	}
	staged, stagedPath, err := createManagedWindowsStage(filepath.Dir(path))
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
	for remaining := content; len(remaining) > 0; {
		written, writeErr := staged.Write(remaining)
		if writeErr != nil {
			return writeErr
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	if err := staged.Sync(); err != nil {
		return err
	}
	stageSnapshot, err := validateManagedWindowsStage(staged, stagedPath, content)
	if err != nil {
		return err
	}
	if privateConfigWindowsBeforePublish != nil {
		privateConfigWindowsBeforePublish(path)
	}
	if err := revalidatePrivateConfigWindowsDirectory(filepath.Dir(path), directoryIdentity); err != nil {
		return err
	}
	current, currentExists, currentFile, err := openPrivateConfigWindowsTarget(path)
	if err != nil {
		return err
	}
	if currentFile != nil {
		if err := currentFile.Close(); err != nil {
			return err
		}
	}
	if currentExists != exists || exists && current.identity != before.identity {
		return errors.New("fleet config target changed before publication")
	}
	if finalStage, err := validateManagedWindowsStage(staged, stagedPath, content); err != nil ||
		finalStage.identity != stageSnapshot.identity || finalStage.digest != stageSnapshot.digest {
		if err != nil {
			return err
		}
		return errors.New("fleet config stage changed before publication")
	}

	if !exists {
		renameHandle, err := reopenManagedWindowsHandle(stageHandle, windows.DELETE|windows.READ_CONTROL)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(renameHandle)
		if err := renameManagedWindowsHandleNoReplace(renameHandle, directoryHandle, filepath.Base(path)); err != nil {
			return fmt.Errorf("create fleet config without replacement: %w", err)
		}
		publishedSnapshot, err := readManagedWindowsSnapshot(path)
		if err != nil || publishedSnapshot.identity != stageSnapshot.identity ||
			publishedSnapshot.digest != stageSnapshot.digest || !bytes.Equal(publishedSnapshot.content, content) {
			return errors.Join(errors.New("created fleet config changed at publication"), err)
		}
		published = true
		return nil
	}

	backupPath, err := unusedPrivateConfigWindowsPath(filepath.Dir(path), "backup")
	if err != nil {
		return err
	}
	targetRenameHandle, err := reopenManagedWindowsHandle(windows.Handle(target.Fd()), windows.DELETE|windows.READ_CONTROL)
	if err != nil {
		return fmt.Errorf("reopen validated fleet config for backup: %w", err)
	}
	defer windows.CloseHandle(targetRenameHandle)
	if err := renameManagedWindowsHandleNoReplace(targetRenameHandle, directoryHandle, filepath.Base(backupPath)); err != nil {
		return fmt.Errorf("move validated fleet config to backup: %w", err)
	}
	backupSnapshot, err := readManagedWindowsSnapshot(backupPath)
	if err != nil || backupSnapshot.identity != before.identity || backupSnapshot.digest != before.digest {
		return errors.Join(errors.New("fleet config backup changed at publication boundary"), err)
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("fleet config target appeared after backup")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if finalStage, err := validateManagedWindowsStage(staged, stagedPath, content); err != nil ||
		finalStage.identity != stageSnapshot.identity || finalStage.digest != stageSnapshot.digest {
		if err != nil {
			return err
		}
		return errors.New("fleet config stage changed after target backup")
	}
	if err := revalidatePrivateConfigWindowsDirectory(filepath.Dir(path), directoryIdentity); err != nil {
		return err
	}
	stageRenameHandle, err := reopenManagedWindowsHandle(stageHandle, windows.DELETE|windows.READ_CONTROL)
	if err != nil {
		return fmt.Errorf("reopen validated fleet config stage: %w", err)
	}
	defer windows.CloseHandle(stageRenameHandle)
	if err := renameManagedWindowsHandleNoReplace(stageRenameHandle, directoryHandle, filepath.Base(path)); err != nil {
		return fmt.Errorf("publish fleet config after backup: %w", err)
	}
	published = true
	publishedSnapshot, publishedErr := readManagedWindowsSnapshot(path)
	backupSnapshot, backupErr := readManagedWindowsSnapshot(backupPath)
	parentErr := revalidatePrivateConfigWindowsDirectory(filepath.Dir(path), directoryIdentity)
	if publishedErr != nil || backupErr != nil || parentErr != nil ||
		publishedSnapshot.identity != stageSnapshot.identity || publishedSnapshot.digest != stageSnapshot.digest ||
		backupSnapshot.identity != before.identity {
		return errors.Join(errors.New("fleet config changed at guarded replacement boundary"), publishedErr, backupErr, parentErr)
	}
	if err := deleteManagedWindowsHandle(targetRenameHandle); err != nil {
		return fmt.Errorf("delete validated fleet config backup: %w", err)
	}
	// Delete disposition is the commit point. Once it succeeds, returning an
	// error would make deferred stage cleanup remove the already-published target.
	_ = target.Close()
	target = nil
	published = true
	return nil
}

func openPrivateConfigWindowsTarget(path string) (managedWindowsSnapshot, bool, *os.File, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return managedWindowsSnapshot{}, false, nil, nil
	} else if err != nil {
		return managedWindowsSnapshot{}, false, nil, err
	}
	if err := rejectManagedWindowsReparsePath(path, false); err != nil {
		return managedWindowsSnapshot{}, false, nil, err
	}
	handle, information, err := openManagedWindowsPath(
		path, false, windows.GENERIC_READ|windows.READ_CONTROL,
	)
	if err != nil {
		return managedWindowsSnapshot{}, false, nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return managedWindowsSnapshot{}, false, nil, errors.New("cannot wrap fleet config target handle")
	}
	if information.NumberOfLinks != 1 {
		_ = file.Close()
		return managedWindowsSnapshot{}, false, nil, fmt.Errorf("fleet config %s has %d hard links, want 1", path, information.NumberOfLinks)
	}
	if err := validateManagedWindowsDescriptor(handle, false); err != nil {
		_ = file.Close()
		return managedWindowsSnapshot{}, false, nil, err
	}
	snapshot, err := readManagedWindowsHandleSnapshot(file, path, windowsIdentity(information))
	if err != nil {
		_ = file.Close()
		return managedWindowsSnapshot{}, false, nil, err
	}
	byName, err := readManagedWindowsSnapshot(path)
	if err != nil || byName.identity != snapshot.identity || byName.digest != snapshot.digest {
		_ = file.Close()
		return managedWindowsSnapshot{}, false, nil, errors.Join(errors.New("fleet config name changed while opening"), err)
	}
	return snapshot, true, file, nil
}

func revalidatePrivateConfigWindowsDirectory(path string, expected managedWindowsIdentity) error {
	handle, information, err := openManagedWindowsPath(path, true, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if windowsIdentity(information) != expected {
		return errors.New("fleet config parent changed during publication")
	}
	return nil
}

func unusedPrivateConfigWindowsPath(directory, kind string) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		path := filepath.Join(directory, ".dev-remotes-"+kind+"-"+hex.EncodeToString(random[:])+".tmp")
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("cannot allocate private fleet config backup path")
}

func rollbackPrivateConfigWindowsReplacement(path, backupPath string, staged *os.File, stageSnapshot managedWindowsSnapshot) error {
	current, err := readManagedWindowsSnapshot(path)
	if err != nil || current.identity != stageSnapshot.identity || current.digest != stageSnapshot.digest {
		return errors.Join(errors.New("cannot safely roll back changed fleet config target"), err)
	}
	failedPath, err := unusedPrivateConfigWindowsPath(filepath.Dir(path), "failed-publication")
	if err != nil {
		return err
	}
	if err := replaceManagedWindowsFileAtomic(path, backupPath, failedPath); err != nil {
		return err
	}
	if failed, err := readManagedWindowsSnapshot(failedPath); err != nil || failed.identity != stageSnapshot.identity {
		return errors.Join(errors.New("fleet config rollback did not preserve failed publication"), err)
	}
	return nil
}
