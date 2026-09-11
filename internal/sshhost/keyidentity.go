package sshhost

import "io/fs"

// A key review is bound to change time as well as inode, mode, size and mtime.
// Restoring mtime after an in-place write cannot preserve a reviewed key proof.
type selectedKeyIdentity struct {
	secureFileIdentity
	nativeChangeTime int64
}

func sameSelectedKeyFileInfo(left, right fs.FileInfo) bool {
	return stableFileInfo(left, right) && permissionSameChangeTime(left, right)
}

func sameSelectedKeyIdentity(left, right selectedKeyIdentity) bool {
	return left.path == right.path && sameSelectedKeyFileInfo(left.info, right.info) &&
		left.nativeChangeTime == right.nativeChangeTime
}

func (s *Service) inspectSelectedKeyIdentity(path string) (selectedKeyIdentity, error) {
	identity, err := s.inspectSecureIdentity(path)
	if err != nil {
		return selectedKeyIdentity{}, err
	}
	changeTime, err := selectedKeyNativeChangeTime(identity)
	if err != nil {
		return selectedKeyIdentity{}, err
	}
	return selectedKeyIdentity{secureFileIdentity: identity, nativeChangeTime: changeTime}, nil
}

func (s *Service) revalidateSelectedKeyIdentity(expected selectedKeyIdentity) error {
	current, err := s.inspectSelectedKeyIdentity(expected.path)
	if err != nil {
		return err
	}
	if !sameSelectedKeyIdentity(expected, current) {
		return ErrSourceChanged
	}
	return nil
}
