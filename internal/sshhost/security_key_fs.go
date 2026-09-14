package sshhost

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// hardenCapturedSecurityKeyFile validates the originally observed object before
// changing metadata. Both hardening and verification use the same descriptor;
// a changed pathname never supplies replacement mutation or cleanup authority.
func (s *Service) hardenCapturedSecurityKeyFile(path string, expected fs.FileInfo) (*stagedFile, error) {
	if expected == nil {
		return nil, ErrSourceChanged
	}
	if s.beforeSecurityKeyHarden != nil {
		s.beforeSecurityKeyHarden(path)
	}
	if err := s.validateSSHPath(path, false); err != nil {
		return nil, err
	}
	parent, name := filepath.Dir(path), filepath.Base(path)
	root, held, err := openHeldDirectory(parent, true)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = root.Close()
		}
	}()
	before, err := root.Lstat(name)
	if err != nil || !sameSelectedKeyFileInfo(expected, before) {
		return nil, ErrSourceChanged
	}
	file, err := platformOpenNoFollowReadWrite(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameSelectedKeyFileInfo(expected, opened) {
		return nil, ErrSourceChanged
	}
	if err := platformValidatePublicFile(path, file, opened); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigBytes {
		return nil, ErrUnsafePath
	}
	read, err := file.Stat()
	if err != nil || !sameSelectedKeyFileInfo(expected, read) {
		return nil, ErrSourceChanged
	}
	current, err := root.Lstat(name)
	if err != nil || !sameSelectedKeyFileInfo(expected, current) {
		return nil, ErrSourceChanged
	}
	if err := verifyHeldDirectory(parent, held, true); err != nil {
		return nil, err
	}
	if err := platformApplyPrivateFile(path, file); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	hardened, err := file.Stat()
	if err != nil || !os.SameFile(expected, hardened) || expected.Size() != hardened.Size() || !expected.ModTime().Equal(hardened.ModTime()) {
		return nil, ErrSourceChanged
	}
	if err := platformValidatePrivateFile(path, file, hardened); err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	verified, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || !bytes.Equal(data, verified) {
		return nil, ErrSourceChanged
	}
	after, err := file.Stat()
	if err != nil || !sameSelectedKeyFileInfo(hardened, after) {
		return nil, ErrSourceChanged
	}
	current, err = root.Lstat(name)
	if err != nil || !sameSelectedKeyFileInfo(after, current) {
		return nil, ErrSourceChanged
	}
	if err := verifyHeldDirectory(parent, held, true); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	keep = true
	return &stagedFile{dir: parent, name: name, root: root, held: held,
		snapshot: fileSnapshot{path: path, exists: true, info: after, data: data, digest: digestBytes(data)}}, nil
}
