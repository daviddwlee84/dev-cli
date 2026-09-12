//go:build !darwin && !linux && !windows

package tui

import (
	"fmt"
	"runtime"
)

func flushTerminalFD(uintptr) error {
	return fmt.Errorf("terminal input reset is unsupported on %s", runtime.GOOS)
}
