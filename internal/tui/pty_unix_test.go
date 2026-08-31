//go:build !windows

package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"
)

const tuiPTYHelperEnv = "DEV_TUI_PTY_HELPER"

func TestInitialFrameAndQuitWhileLocalLoadIsBlocked(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestTUIBlockedLoadHelper$")
	command.Env = append(os.Environ(), tuiPTYHelperEnv+"=1", "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 30, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()

	chunks := make(chan []byte, 16)
	readErr := make(chan error, 1)
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				chunks <- append([]byte(nil), buffer[:n]...)
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	var output bytes.Buffer
	frameDeadline := time.NewTimer(15 * time.Second)
	defer frameDeadline.Stop()
	for !bytes.Contains(output.Bytes(), []byte("TASKS")) ||
		!bytes.Contains(output.Bytes(), []byte("Loading tasks")) {
		select {
		case chunk := <-chunks:
			output.Write(chunk)
		case err := <-readErr:
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				t.Fatalf("read PTY before initial frame: %v\n%s", err, output.Bytes())
			}
			t.Fatalf("PTY closed before initial frame:\n%s", output.Bytes())
		case <-frameDeadline.C:
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("initial frame did not appear while producer was blocked:\n%s", output.Bytes())
		}
	}
	if _, err := terminal.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}

	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("TUI did not exit cleanly after q: %v\n%s", err, output.Bytes())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		<-exited
		t.Fatalf("TUI did not process q while producer was blocked:\n%s", output.Bytes())
	}
}

func TestTUIBlockedLoadHelper(t *testing.T) {
	if os.Getenv(tuiPTYHelperEnv) != "1" {
		t.Skip("helper process")
	}
	ctx := t.Context()
	results := make(chan LocalResult)
	actions := Actions{Local: LocalActions{Start: func(context.Context, LocalLoadRequest) LocalLoad {
		return LocalLoad{ID: 1, Results: results}
	}}}
	model := New(actions, nil, nil).WithContext(ctx).BeginLoading()
	if _, err := tea.NewProgram(model, tea.WithAltScreen()).Run(); err != nil {
		t.Fatal(err)
	}
}
