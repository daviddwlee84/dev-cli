//go:build windows

package experiment

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
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
	sourceVolume, err := windowsVolumePath(sourceAncestor)
	if err != nil {
		return false, fmt.Errorf("identify source volume: %w", err)
	}
	destinationVolume, err := windowsVolumePath(destinationAncestor)
	if err != nil {
		return false, fmt.Errorf("identify destination volume: %w", err)
	}
	return sameWindowsVolumePath(sourceVolume, destinationVolume), nil
}

func windowsVolumePath(path string) (string, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(pathPointer, &buffer[0], uint32(len(buffer))); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer), nil
}

func sameWindowsVolumePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
