//go:build unix && !darwin && !linux

package sshhost

import "os"

func platformFileFlags(*os.File) (uint32, error)  { return 0, nil }
func platformFlagsRoundTrip(uint32) error         { return nil }
func platformSetFileFlags(*os.File, uint32) error { return nil }
