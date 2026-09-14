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
	if !os.SameFile(a, b) || a.Mode() != b.Mode() || !nativeSameOwner(a, b) {
		return false
	}
	// Directory ctime includes unrelated child/link-count changes. Its ACL is
	// observed explicitly and confirmed independently, not inferred from that
	// aggregate clock. Regular files and links retain the stricter ctime guard.
	return a.IsDir() && b.IsDir() || nativeSameChangeTime(a, b)
}

func readStableNativeACL(read func() (nativeACLObservation, error)) (nativeACLObservation, error) {
	first, err := read()
	if err != nil || !first.safe {
		return nativeACLObservation{}, ErrNativeContext
	}
	second, err := read()
	if err != nil || !second.safe {
		return nativeACLObservation{}, ErrNativeContext
	}
	if first != second {
		return nativeACLObservation{}, ErrStale
	}
	return second, nil
}
