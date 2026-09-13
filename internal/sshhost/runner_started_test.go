package sshhost

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRunnerObservesOnlyActuallyStartedChild(t *testing.T) {
	if os.Getenv("DEV_TEST_SSH_STARTED_CHILD") == "1" {
		os.Exit(7)
	}
	calls := 0
	started := func(at time.Time) {
		if at.IsZero() {
			t.Fatal("zero start")
		}
		calls++
	}
	_, err := (ExecRunner{}).Run(t.Context(), RunRequest{Name: "dev-test-missing-started-binary", OnStarted: started})
	if err == nil || calls != 0 {
		t.Fatal("observed missing process")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r, err := (ExecRunner{}).Run(t.Context(), RunRequest{Name: executable, Args: []string{"-test.run=^TestRunnerObservesOnlyActuallyStartedChild$"}, Env: []string{"DEV_TEST_SSH_STARTED_CHILD=1"}, OnStarted: started})
	if err != nil || r.ExitCode != 7 || calls != 1 {
		t.Fatalf("%+v %v calls=%d", r, err, calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = (ExecRunner{}).Run(ctx, RunRequest{Name: executable, OnStarted: started})
	if err == nil || calls != 1 {
		t.Fatal("canceled process observed")
	}
}
