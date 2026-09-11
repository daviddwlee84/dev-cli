//go:build windows

package sshhost

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	command := `$child=Start-Process -FilePath "$env:SystemRoot\System32\cmd.exe" -ArgumentList '/c','ping -n 30 127.0.0.1 >NUL' -PassThru;[IO.File]::WriteAllText($env:DEV_CLI_PID_FILE,$child.Id.ToString());Start-Sleep -Seconds 30`
	finished := make(chan error, 1)
	go func() {
		_, err := (ExecRunner{}).Run(ctx, RunRequest{
			Name: "powershell.exe", Args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command},
			Env: []string{"DEV_CLI_PID_FILE=" + pidPath}, Display: "Job Object cancellation fixture",
		})
		finished <- err
	}()
	// Arm cancellation only after the descendant exists. A cold PowerShell
	// startup on hosted Windows can itself exceed the old five-second budget.
	var pidBytes []byte
	startupDeadline := time.NewTimer(20 * time.Second)
	defer startupDeadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	ready := false
	for !ready {
		select {
		case err := <-finished:
			t.Fatalf("fixture exited before recording its child PID: %v", err)
		case <-startupDeadline.C:
			cancel()
			<-finished
			t.Fatal("fixture did not start its descendant within 20 seconds")
		case <-ticker.C:
			data, err := os.ReadFile(pidPath)
			if err == nil && len(strings.TrimSpace(string(data))) > 0 {
				pidBytes = data
				ready = true
			}
		}
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want cancellation", err)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil || pid <= 0 {
		t.Fatalf("child PID file = %q, err %v", pidBytes, parseErr)
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
