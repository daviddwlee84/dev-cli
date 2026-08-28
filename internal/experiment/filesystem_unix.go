//go:build !windows

package experiment

import (
	"os"
	"syscall"
)

func sameFilesystem(source, destination string) (bool, error) {
	sourceAncestor, err := nearestExisting(source)
	if err != nil {
		return false, err
	}
	destinationAncestor, err := nearestExisting(destination)
	if err != nil {
		return false, err
	}
	sourceInfo, err := os.Stat(sourceAncestor)
	if err != nil {
		return false, err
	}
	destinationInfo, err := os.Stat(destinationAncestor)
	if err != nil {
		return false, err
	}
	sourceStat, sourceOK := sourceInfo.Sys().(*syscall.Stat_t)
	destinationStat, destinationOK := destinationInfo.Sys().(*syscall.Stat_t)
	if !sourceOK || !destinationOK {
		// os.Rename remains the final authority on platforms whose FileInfo does
		// not expose a Unix-style device number.
		return true, nil
	}
	return sourceStat.Dev == destinationStat.Dev, nil
}
