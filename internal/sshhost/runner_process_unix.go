//go:build unix

package sshhost

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

type platformProcessTree struct {
	process *os.Process
	pid     int
	grouped bool
}

func platformPrepareCommand(cmd *exec.Cmd, interactive bool) {
	if !interactive {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}

func platformAttachProcess(cmd *exec.Cmd, interactive bool) (*platformProcessTree, error) {
	return &platformProcessTree{
		process: cmd.Process,
		pid:     cmd.Process.Pid,
		grouped: !interactive,
	}, nil
}

func (p *platformProcessTree) terminate() error {
	if p == nil || p.process == nil || p.pid <= 0 {
		return nil
	}
	if !p.grouped {
		err := p.process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	err := unix.Kill(-p.pid, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func (*platformProcessTree) close() error { return nil }
