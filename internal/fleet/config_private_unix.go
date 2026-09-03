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
	"syscall"
)

func checkPrivateConfigFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s contains a plaintext SSH password and must have mode 0600", path)
	}
	return nil
}

var privateConfigUnixBeforePublish func(path string)

// WritePrivateConfigFile creates or explicitly overwrites a primary fleet
// config using a same-directory private stage. Existing symlinks, foreign
// owners, and hard-linked files are rejected before any target bytes change.
func WritePrivateConfigFile(path string, content []byte, overwrite bool) error {
	directoryPath := filepath.Dir(path)
	directoryBefore, err := os.Lstat(directoryPath)
	if err != nil {
		return err
	}
	if directoryBefore.Mode()&os.ModeSymlink != 0 || !directoryBefore.IsDir() {
		return fmt.Errorf("fleet config parent %s is not a direct directory", directoryPath)
	}
	root, err := os.OpenRoot(directoryPath)
	if err != nil {
		return err
	}
	defer root.Close()
	directoryOpened, err := root.Stat(".")
	if err != nil || !os.SameFile(directoryBefore, directoryOpened) {
		return errors.Join(errors.New("fleet config parent changed while opening"), err)
	}
	directoryFile, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directoryFile.Close()

	targetName := filepath.Base(path)
	target, exists, err := inspectPrivateConfigUnixTarget(root, targetName, path)
	if err != nil {
		return err
	}
	if exists && !overwrite {
		return fs.ErrExist
	}
	stageName, stageInfo, err := createPrivateConfigUnixStage(root, content)
	if err != nil {
		return err
	}
	defer removePrivateConfigUnixStage(root, stageName, stageInfo)

	if privateConfigUnixBeforePublish != nil {
		privateConfigUnixBeforePublish(path)
	}
	if err := revalidatePrivateConfigUnixParent(directoryPath, directoryBefore, root); err != nil {
		return err
	}
	current, currentExists, err := inspectPrivateConfigUnixTarget(root, targetName, path)
	if err != nil {
		return err
	}
	if currentExists != exists || exists && !os.SameFile(current, target) {
		return fmt.Errorf("fleet config target changed before publication")
	}
	if err := validatePrivateConfigUnixStage(root, stageName, stageInfo, content); err != nil {
		return err
	}
	if !exists {
		if err := renameManagedUnixNoReplace(directoryFile, stageName, targetName); err != nil {
			return fmt.Errorf("create fleet config without replacement: %w", err)
		}
		published, _, err := inspectPrivateConfigUnixTarget(root, targetName, path)
		if err != nil || !os.SameFile(published, stageInfo) {
			return errors.Join(errors.New("created fleet config changed at publication"), err)
		}
		return directoryFile.Sync()
	}

	if err := exchangeManagedUnixFiles(directoryFile, stageName, targetName); err != nil {
		return fmt.Errorf("atomically replace fleet config: %w", err)
	}
	published, publishedErr := root.Lstat(targetName)
	displaced, displacedErr := root.Lstat(stageName)
	parentErr := revalidatePrivateConfigUnixParent(directoryPath, directoryBefore, root)
	if publishedErr != nil || displacedErr != nil || parentErr != nil ||
		!os.SameFile(published, stageInfo) || !os.SameFile(displaced, target) {
		rollbackErr := rollbackPrivateConfigUnixExchange(root, directoryFile, stageName, targetName, stageInfo)
		return errors.Join(errors.New("fleet config changed at replacement boundary"), publishedErr, displacedErr, parentErr, rollbackErr)
	}
	if err := root.Remove(stageName); err != nil {
		rollbackErr := rollbackPrivateConfigUnixExchange(root, directoryFile, stageName, targetName, stageInfo)
		return errors.Join(fmt.Errorf("remove displaced fleet config: %w", err), rollbackErr)
	}
	return directoryFile.Sync()
}

func inspectPrivateConfigUnixTarget(root *os.Root, name, path string) (fs.FileInfo, bool, error) {
	before, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, false, fmt.Errorf("fleet config %s is not a direct regular file", path)
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return nil, false, fmt.Errorf("fleet config %s must be current-user-owned with one hard link", path)
	}
	opened, err := root.Open(name)
	if err != nil {
		return nil, false, err
	}
	openedInfo, statErr := opened.Stat()
	closeErr := opened.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(before, openedInfo) {
		return nil, false, errors.Join(errors.New("fleet config changed while validating"), statErr, closeErr)
	}
	after, err := root.Lstat(name)
	if err != nil || !os.SameFile(openedInfo, after) {
		return nil, false, errors.Join(errors.New("fleet config changed after validating"), err)
	}
	return openedInfo, true, nil
}

func createPrivateConfigUnixStage(root *os.Root, content []byte) (string, fs.FileInfo, error) {
	for attempt := 0; attempt < 32; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		name := ".dev-remotes-" + hex.EncodeToString(random[:]) + ".tmp"
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		failed := true
		defer func() {
			if failed {
				_ = file.Close()
				_ = root.Remove(name)
			}
		}()
		if err := file.Chmod(0o600); err != nil {
			return "", nil, err
		}
		for remaining := content; len(remaining) > 0; {
			written, writeErr := file.Write(remaining)
			if writeErr != nil {
				return "", nil, writeErr
			}
			if written == 0 {
				return "", nil, io.ErrShortWrite
			}
			remaining = remaining[written:]
		}
		if err := file.Sync(); err != nil {
			return "", nil, err
		}
		info, err := file.Stat()
		if err != nil {
			return "", nil, err
		}
		if err := file.Close(); err != nil {
			return "", nil, err
		}
		failed = false
		return name, info, nil
	}
	return "", nil, errors.New("cannot allocate private fleet config stage")
}

func validatePrivateConfigUnixStage(root *os.Root, name string, expected fs.FileInfo, content []byte) error {
	before, err := root.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		before.Mode().Perm() != 0o600 || !os.SameFile(before, expected) {
		return errors.Join(errors.New("private fleet config stage changed before publication"), err)
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return errors.New("private fleet config stage has unsafe ownership or links")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	opened, statErr := file.Stat()
	read, readErr := io.ReadAll(file)
	closeErr := file.Close()
	after, afterErr := root.Lstat(name)
	if statErr != nil || readErr != nil || closeErr != nil || afterErr != nil ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) || !bytes.Equal(read, content) {
		return errors.Join(errors.New("private fleet config stage identity or content changed"), statErr, readErr, closeErr, afterErr)
	}
	return nil
}

func removePrivateConfigUnixStage(root *os.Root, name string, expected fs.FileInfo) {
	current, err := root.Lstat(name)
	if err == nil && os.SameFile(current, expected) {
		_ = root.Remove(name)
	}
}

func revalidatePrivateConfigUnixParent(path string, expected fs.FileInfo, root *os.Root) error {
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	rooted, err := root.Stat(".")
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(current, expected) || !os.SameFile(rooted, expected) {
		return errors.New("fleet config parent changed during publication")
	}
	return nil
}

func rollbackPrivateConfigUnixExchange(root *os.Root, directoryFile *os.File, stageName, targetName string, stageInfo fs.FileInfo) error {
	currentTarget, targetErr := root.Lstat(targetName)
	_, stageErr := root.Lstat(stageName)
	if targetErr != nil || stageErr != nil || !os.SameFile(currentTarget, stageInfo) {
		return errors.Join(errors.New("cannot safely roll back fleet config exchange"), targetErr, stageErr)
	}
	if err := exchangeManagedUnixFiles(directoryFile, stageName, targetName); err != nil {
		return err
	}
	return directoryFile.Sync()
}
