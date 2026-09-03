//go:build windows

package sshhost

import (
	"errors"
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

type platformProcessTree struct {
	job     windows.Handle
	process *os.Process
}

func platformPrepareCommand(*exec.Cmd, bool) {}

func platformAttachProcess(cmd *exec.Cmd, interactive bool) (*platformProcessTree, error) {
	if interactive {
		return &platformProcessTree{process: cmd.Process}, nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = windows.CloseHandle(job)
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		return nil, err
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return nil, err
	}
	keep = true
	return &platformProcessTree{job: job, process: cmd.Process}, nil
}

func (p *platformProcessTree) terminate() error {
	if p == nil || p.process == nil {
		return nil
	}
	if p.job == 0 || p.job == windows.InvalidHandle {
		err := p.process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	return windows.TerminateJobObject(p.job, 1)
}

func (p *platformProcessTree) close() error {
	if p == nil || p.job == 0 || p.job == windows.InvalidHandle {
		return nil
	}
	err := windows.CloseHandle(p.job)
	p.job = 0
	return err
}
