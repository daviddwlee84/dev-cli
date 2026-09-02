package sshhost

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type secureFileIdentity struct {
	path string
	info fs.FileInfo
}

func (s *Service) resolveSSHKeyPath(value string) (string, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("SSH key path is empty or contains NUL: %w", ErrUnsafePath)
	}
	resolved := value
	switch {
	case value == "~":
		resolved = s.paths.Home
	case strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`):
		resolved = filepath.Join(s.paths.Home, filepath.FromSlash(value[2:]))
	case value == "%d":
		resolved = s.paths.Home
	case strings.HasPrefix(value, "%d/") || strings.HasPrefix(value, `%d\`):
		resolved = filepath.Join(s.paths.Home, filepath.FromSlash(value[3:]))
	case value == "${HOME}":
		resolved = s.paths.Home
	case strings.HasPrefix(value, "${HOME}/") || strings.HasPrefix(value, `${HOME}\`):
		resolved = filepath.Join(s.paths.Home, filepath.FromSlash(value[len("${HOME}/"):]))
	case strings.HasPrefix(value, "~") || strings.ContainsAny(value, "%$"):
		return "", fmt.Errorf("SSH key path uses unsupported expansion: %w", ErrUnsafePath)
	}
	if !filepath.IsAbs(resolved) {
		absolute, err := filepath.Abs(resolved)
		if err != nil {
			return "", err
		}
		resolved = absolute
	}
	resolved = filepath.Clean(resolved)
	if !s.pathWithinSSH(resolved) {
		return "", fmt.Errorf("SSH key path is outside %s: %w", s.paths.SSHDir, ErrUnsafePath)
	}
	return resolved, nil
}

func (s *Service) pathWithinSSH(path string) bool {
	path = filepath.Clean(path)
	relative, err := filepath.Rel(s.paths.SSHDir, path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return relative != "" && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (s *Service) validateSSHPath(path string, allowMissingFinal bool) error {
	if !s.pathWithinSSH(path) {
		return fmt.Errorf("path is outside the user SSH tree: %w", ErrUnsafePath)
	}
	if err := validateHomeDirectory(s.paths.Home); err != nil {
		return err
	}
	if err := validatePrivateDirectoryPath(s.paths.SSHDir); err != nil {
		return err
	}
	relative, err := filepath.Rel(s.paths.SSHDir, filepath.Clean(path))
	if err != nil {
		return err
	}
	parts := strings.FieldsFunc(relative, func(r rune) bool { return r == '/' || r == '\\' })
	current := s.paths.SSHDir
	for index, part := range parts {
		current = filepath.Join(current, part)
		final := index == len(parts)-1
		info, statErr := os.Lstat(current)
		if final && allowMissingFinal && errors.Is(statErr, fs.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if err := platformRejectReparsePath(current, false); err != nil {
			return err
		}
		if !final {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("SSH key parent is not a direct directory: %w", ErrUnsafePath)
			}
			if err := platformValidateCatalogDirectory(current, info); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePrivateDirectoryPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validatePrivateDirectory(path, info)
}

func (s *Service) validateKeyParent(path string) error {
	probe := filepath.Join(filepath.Clean(path), ".dev-key-parent-probe")
	if !s.pathWithinSSH(probe) {
		return fmt.Errorf("SSH key parent is outside the user SSH tree: %w", ErrUnsafePath)
	}
	if err := s.validateSSHPath(probe, true); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validatePrivateDirectory(path, info)
}

func (s *Service) readPublicKeyFile(path string) (publicKeyRecord, error) {
	path = filepath.Clean(path)
	if err := s.validateSSHPath(path, false); err != nil {
		return publicKeyRecord{}, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return publicKeyRecord{}, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() > MaxPublicKeyLineBytes {
		return publicKeyRecord{}, fmt.Errorf("public key is not a bounded direct regular file: %w", ErrUnsafePath)
	}
	file, err := platformOpenNoFollow(path)
	if err != nil {
		return publicKeyRecord{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return publicKeyRecord{}, err
	}
	if !stableFileInfo(before, opened) {
		return publicKeyRecord{}, ErrSourceChanged
	}
	if err := platformValidatePublicFile(path, file, opened); err != nil {
		return publicKeyRecord{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxPublicKeyLineBytes+1))
	if err != nil {
		return publicKeyRecord{}, err
	}
	if len(data) > MaxPublicKeyLineBytes {
		return publicKeyRecord{}, fmt.Errorf("public key exceeds %d bytes", MaxPublicKeyLineBytes)
	}
	after, err := file.Stat()
	if err != nil {
		return publicKeyRecord{}, err
	}
	pathAfter, err := os.Lstat(path)
	if err != nil || pathAfter.Mode()&os.ModeSymlink != 0 || !stableFileInfo(opened, after) || !stableFileInfo(after, pathAfter) || after.Size() != int64(len(data)) {
		if err != nil {
			return publicKeyRecord{}, err
		}
		return publicKeyRecord{}, ErrSourceChanged
	}
	return parsePublicKeyRecord(data)
}

func (s *Service) inspectSecureIdentity(path string) (secureFileIdentity, error) {
	path = filepath.Clean(path)
	if err := s.validateSSHPath(path, false); err != nil {
		return secureFileIdentity{}, err
	}
	parent := filepath.Dir(path)
	root, held, err := openHeldDirectory(parent, true)
	if err != nil {
		return secureFileIdentity{}, err
	}
	defer root.Close()
	before, err := root.Lstat(filepath.Base(path))
	if err != nil {
		return secureFileIdentity{}, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return secureFileIdentity{}, fmt.Errorf("identity is not a direct regular file: %w", ErrUnsafePath)
	}
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return secureFileIdentity{}, err
	}
	opened, err := file.Stat()
	if err == nil && !stableFileInfo(before, opened) {
		err = ErrSourceChanged
	}
	if err == nil {
		err = platformValidatePrivateFile(path, file, opened)
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return secureFileIdentity{}, err
	}
	if err := verifyHeldDirectory(parent, held, true); err != nil {
		return secureFileIdentity{}, err
	}
	return secureFileIdentity{path: path, info: opened}, nil
}

func (s *Service) revalidateSecureIdentity(expected secureFileIdentity) error {
	current, err := s.inspectSecureIdentity(expected.path)
	if err != nil {
		return err
	}
	if !stableFileInfo(expected.info, current.info) {
		return ErrSourceChanged
	}
	return nil
}

func (s *Service) inspectPublicDestination(path string) (fileSnapshot, error) {
	path = filepath.Clean(path)
	if err := s.validateSSHPath(path, true); err != nil {
		return fileSnapshot{path: path}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileSnapshot{path: path}, nil
	}
	if err != nil {
		return fileSnapshot{path: path}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fileSnapshot{path: path}, fmt.Errorf("public destination is not a direct regular file: %w", ErrUnsafePath)
	}
	if _, err := s.readPublicKeyFile(path); err != nil {
		return fileSnapshot{path: path}, err
	}
	return fileSnapshot{path: path, exists: true, info: info}, nil
}

func (s *Service) inspectPrivateDestination(path string) (fileSnapshot, error) {
	path = filepath.Clean(path)
	if err := s.validateSSHPath(path, true); err != nil {
		return fileSnapshot{path: path}, err
	}
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileSnapshot{path: path}, nil
	}
	if err != nil {
		return fileSnapshot{path: path}, err
	}
	identity, err := s.inspectSecureIdentity(path)
	if err != nil {
		return fileSnapshot{path: path}, err
	}
	return fileSnapshot{path: path, exists: true, info: identity.info}, nil
}

func (s *Service) hardenAndSyncGeneratedFile(path string) error {
	if err := s.validateSSHPath(path, false); err != nil {
		return err
	}
	file, err := platformOpenNoFollowReadWrite(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = ErrUnsafePath
		}
		return err
	}
	if err := platformApplyPrivateFile(path, file); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	updated, err := file.Stat()
	if err != nil {
		return err
	}
	return platformValidatePrivateFile(path, file, updated)
}

func publicLinesEqual(left, right []byte) bool {
	return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
}
