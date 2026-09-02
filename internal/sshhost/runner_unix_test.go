//go:build unix

package sshhost

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPlatformPrepareCommandKeepsInteractiveChildInForegroundGroup(t *testing.T) {
	interactive := exec.Command("/bin/sh", "-c", "exit 0")
	platformPrepareCommand(interactive, true)
	if interactive.SysProcAttr != nil && interactive.SysProcAttr.Setpgid {
		t.Fatalf("interactive child received a new process group: %#v", interactive.SysProcAttr)
	}

	batch := exec.Command("/bin/sh", "-c", "exit 0")
	platformPrepareCommand(batch, false)
	if batch.SysProcAttr == nil || !batch.SysProcAttr.Setpgid {
		t.Fatalf("batch child has no cancellation process group: %#v", batch.SysProcAttr)
	}
}

func TestExecRunnerCancellationKillsUnixProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	result, err := (ExecRunner{}).Run(ctx, RunRequest{
		Name: "/bin/sh", Args: []string{"-c", "sleep 30 & child=$!; printf '%s\\n' \"$child\"; wait"},
		Display: "process-group cancellation fixture",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want deadline", err)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(result.Stdout)))
	if parseErr != nil || pid <= 0 {
		t.Fatalf("child PID output = %q, err %v", result.Stdout, parseErr)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := unix.Kill(pid, 0)
		if errors.Is(err, unix.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d remains after group cancellation (kill probe %v)", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
