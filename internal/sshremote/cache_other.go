//go:build !linux && !darwin && !windows

package sshremote

import "io/fs"

func cachePlatformSupported() error                 { return ErrUnsafeCache }
func checkCacheAncestor(string, fs.FileInfo) error  { return ErrUnsafeCache }
func checkCacheDirectory(string, fs.FileInfo) error { return ErrUnsafeCache }
func checkCacheFile(string, fs.FileInfo) error      { return ErrUnsafeCache }
func setCachePrivate(string, fs.FileMode) error     { return ErrUnsafeCache }
