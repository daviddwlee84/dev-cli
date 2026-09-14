package sshvault

import (
	"io/fs"
	"os"
)

// Absence and narrowly validated Darwin deny-only ACLs are supported. The full
// accepted native metadata is fingerprinted privately so even a deny-only change
// invalidates reviewed authority. Unknown semantics never become an allow rule.
type nativeACLObservation struct {
	safe   bool
	digest [32]byte
}

func nativePermissionIdentity(a, b fs.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && nativeSameOwner(a, b) && nativeSameChangeTime(a, b)
}
