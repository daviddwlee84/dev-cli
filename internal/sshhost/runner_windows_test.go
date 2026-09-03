//go:build windows

package sshhost

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPlatformAttachProcessDoesNotJobControlInteractiveChild(t *testing.T) {
	command := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	tree, err := platformAttachProcess(command, true)
	if err != nil {
		command.Process.Kill()
		command.Wait()
		t.Fatal(err)
	}
	if tree.job != 0 {
		t.Errorf("interactive child was assigned to Job Object %v", tree.job)
	}
	if err := tree.terminate(); err != nil {
		t.Error(err)
	}
	_ = command.Wait()
	if err := tree.close(); err != nil {
		t.Error(err)
	}
}

func TestExecRunnerCancellationKillsWindowsJobObject(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := `$child=Start-Process -FilePath "$env:SystemRoot\System32\cmd.exe" -ArgumentList '/c','ping -n 30 127.0.0.1 >NUL' -PassThru;[Console]::Out.WriteLine($child.Id);[Console]::Out.Flush();Start-Sleep -Seconds 30`
	result, err := (ExecRunner{}).Run(ctx, RunRequest{
		Name: "powershell.exe", Args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command},
		Display: "Job Object cancellation fixture",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want deadline", err)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(result.Stdout)))
	if parseErr != nil || pid <= 0 {
		t.Fatalf("child PID output = %q, err %v", result.Stdout, parseErr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		process, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if openErr != nil {
			break
		}
		var exitCode uint32
		statusErr := windows.GetExitCodeProcess(process, &exitCode)
		windows.CloseHandle(process)
		if statusErr != nil || exitCode != 259 { // STILL_ACTIVE
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d remains active after Job Object cancellation", pid)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
