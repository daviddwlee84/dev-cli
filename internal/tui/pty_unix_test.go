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
	for _, sequence := range [][]byte{[]byte("\x1b[?1002h"), []byte("\x1b[?1006h")} {
		if !bytes.Contains(output.Bytes(), sequence) {
			t.Fatalf("initial frame did not enable mouse tracking %q:\n%s", sequence, output.Bytes())
		}
	}
	if _, err := terminal.Write([]byte("e")); err != nil {
		t.Fatal(err)
	}
	restoreDeadline := time.NewTimer(5 * time.Second)
	defer restoreDeadline.Stop()
	for bytes.Count(output.Bytes(), []byte("\x1b[?1002h")) < 2 {
		select {
		case chunk := <-chunks:
			output.Write(chunk)
		case err := <-readErr:
			t.Fatalf("PTY closed before subprocess restored mouse tracking: %v\n%s", err, output.Bytes())
		case <-restoreDeadline.C:
			t.Fatalf("subprocess return did not restore mouse tracking:\n%s", output.Bytes())
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

	drain := time.NewTimer(time.Second)
	defer drain.Stop()
drainOutput:
	for {
		select {
		case chunk := <-chunks:
			output.Write(chunk)
		case <-readErr:
			break drainOutput
		case <-drain.C:
			break drainOutput
		}
	}
drainBuffered:
	for {
		select {
		case chunk := <-chunks:
			output.Write(chunk)
		default:
			break drainBuffered
		}
	}
	for _, sequence := range [][]byte{[]byte("\x1b[?1002l"), []byte("\x1b[?1006l")} {
		if !bytes.Contains(output.Bytes(), sequence) {
			t.Fatalf("TUI exit did not disable mouse tracking %q:\n%s", sequence, output.Bytes())
		}
	}
}

func TestTUIBlockedLoadHelper(t *testing.T) {
	if os.Getenv(tuiPTYHelperEnv) != "1" {
		t.Skip("helper process")
	}
	ctx := t.Context()
	results := make(chan LocalResult)
	actions := Actions{
		Local: LocalActions{Start: func(context.Context, LocalLoadRequest) LocalLoad {
			return LocalLoad{ID: 1, Results: results}
		}},
		EditConfig: func() (*exec.Cmd, error) { return exec.Command("true"), nil },
	}
	model := New(actions, nil, nil).WithContext(ctx).BeginLoading()
	if _, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		t.Fatal(err)
	}
}
