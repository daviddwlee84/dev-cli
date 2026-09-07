//go:build windows

package agentinterop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"unsafe"

	"golang.org/x/sys/windows"
)

func executeMCP(ctx context.Context, spec launchSpec) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, spec.Program, spec.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Cancel = func() error { return windows.TerminateJobObject(job, 1) }
	if err = cmd.Start(); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	err = windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	if ctx.Err() != nil {
		_ = windows.TerminateJobObject(job, 1)
	}
	err = cmd.Wait()
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		os.Exit(exited.ExitCode())
	}
	return err
}
