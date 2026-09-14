//go:build !unix

package sshvault

import "io/fs"

// Native profile execution stays unavailable without a verified local identity
// and filesystem ownership contract (including native Windows ACLs).
func nativeOwned(fs.FileInfo, bool) bool            { return false }
func nativeLinkOwned(fs.FileInfo) bool              { return false }
func nativeSameOwner(fs.FileInfo, fs.FileInfo) bool { return false }
