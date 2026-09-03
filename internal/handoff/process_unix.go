//go:build !windows

package handoff

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureProcessTree gives the launcher its own process group and replaces
// CommandContext's single-process cancellation with a group kill. Otherwise a
// timed-out shell can exit while its reparented coding agent keeps modifying
// the checkout after dev reports completion and removes the prompt file.
func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
