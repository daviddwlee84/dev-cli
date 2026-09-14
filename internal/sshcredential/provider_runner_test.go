package sshcredential

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

const (
	providerRunnerHelperMode = "DEV_SSHCREDENTIAL_RUNNER_TEST_MODE"
	providerRunnerControl    = "DEV_SSHCREDENTIAL_RUNNER_TEST_CONTROL"
)

func TestNativeProviderRunnerBoundsInheritedPipeWait(t *testing.T) {
	for _, mode := range []string{"exit", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal("locate own test executable")
			}
			listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal("listen for own pipe-holder process")
			}
			t.Setenv(providerRunnerHelperMode, mode)
			t.Setenv(providerRunnerControl, listener.Addr().String())
			ctx, cancel := context.WithCancel(context.Background())
			type outcome struct {
				body []byte
				err  error
			}
			done := make(chan outcome, 1)
			go func() {
				body, err := (NativeRunner{}).Run(ctx, executable, []string{"-test.run=^TestNativeProviderRunnerHelperProcess$"}, nil)
				done <- outcome{body: body, err: err}
			}()
			var control net.Conn
			completed := false
			t.Cleanup(func() {
				// Release only the subprocess created for this test. No PID lookup
				// or unrelated process termination is involved.
				if control != nil {
					_ = control.Close()
				}
				_ = listener.Close()
				cancel()
				if !completed {
					select {
					case result := <-done:
						Wipe(result.body)
					case <-time.After(5 * time.Second):
						t.Error("own runner did not finish after test cleanup")
					}
				}
			})
			if listener.SetDeadline(time.Now().Add(10*time.Second)) != nil {
				t.Fatal("set own listener deadline")
			}
			control, err = listener.Accept()
			if err != nil {
				t.Fatal("own pipe-holder did not connect")
			}
			if control.SetReadDeadline(time.Now().Add(5*time.Second)) != nil {
				t.Fatal("set own helper handshake deadline")
			}
			var ready [1]byte
			if _, err := io.ReadFull(control, ready[:]); err != nil || ready[0] != 'R' {
				t.Fatal("own pipe-holder did not become ready")
			}
			if mode == "cancel" {
				cancel()
			}
			// The holder remains alive with inherited stdout/stderr open until
			// cleanup closes its control socket. Wait must not depend on that.
			select {
			case result := <-done:
				completed = true
				defer Wipe(result.body)
				if !errors.Is(result.err, ErrUnavailable) || result.err != ErrUnavailable || len(result.body) != 0 {
					t.Fatal("pipe cleanup failure exposed output or a native error")
				}
			case <-time.After(7 * time.Second):
				t.Fatal("provider runner waited for descendant-held output pipes")
			}
		})
	}
}

// Only a copy of this test executable is launched. The helper never invokes a
// provider, inspects user processes, or relies on a shell utility.
func TestNativeProviderRunnerHelperProcess(t *testing.T) {
	mode := os.Getenv(providerRunnerHelperMode)
	if mode == "" {
		return
	}
	if mode == "holder" {
		control, err := net.DialTimeout("tcp4", os.Getenv(providerRunnerControl), 5*time.Second)
		if err != nil {
			os.Exit(2)
		}
		_ = control.SetDeadline(time.Now().Add(20 * time.Second))
		if _, err = control.Write([]byte{'R'}); err != nil {
			os.Exit(2)
		}
		_, _ = io.Copy(io.Discard, control)
		_ = control.Close()
		os.Exit(0)
	}
	if mode != "exit" && mode != "cancel" {
		os.Exit(2)
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	child := exec.Command(executable, "-test.run=^TestNativeProviderRunnerHelperProcess$")
	child.Env = append(os.Environ(), providerRunnerHelperMode+"=holder")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if child.Start() != nil {
		os.Exit(2)
	}
	_, _ = io.WriteString(os.Stdout, "provider-output-must-be-discarded")
	if mode == "cancel" {
		time.Sleep(20 * time.Second)
	}
	os.Exit(0)
}
