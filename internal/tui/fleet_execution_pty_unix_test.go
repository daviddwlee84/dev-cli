//go:build !windows

package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

const fleetExecutionPTYEnv = "DEV_FLEET_EXEC_PTY_HELPER"

func TestFleetExecutionPTYDoesNotReuseNativeTypeahead(t *testing.T) {
	for _, input := range []struct{ name, trigger, data string }{{"CR", "\r", "\r"}, {"CRLF", "\r", "\r\n"}, {"burst", "\r", strings.Repeat("\r", 32)}, {"decoded-burst", strings.Repeat("\r", 128), ""}} {
		t.Run(input.name, func(t *testing.T) {
			controlRead, controlWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer controlRead.Close()
			defer controlWrite.Close()
			command := exec.Command(os.Args[0], "-test.run=^TestFleetExecutionPTYHelper$")
			command.Env = append(os.Environ(), fleetExecutionPTYEnv+"=1", "TERM=xterm-256color")
			command.ExtraFiles = []*os.File{controlRead}
			terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 30, Cols: 120})
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
			chunks := make(chan []byte, 128)
			go func() {
				buffer := make([]byte, 4096)
				for {
					n, err := terminal.Read(buffer)
					if n > 0 {
						chunks <- append([]byte(nil), buffer[:n]...)
					}
					if err != nil {
						close(chunks)
						return
					}
				}
			}()
			var output bytes.Buffer
			waitFor := func(marker string) {
				t.Helper()
				deadline := time.NewTimer(10 * time.Second)
				defer deadline.Stop()
				for !bytes.Contains(output.Bytes(), []byte(marker)) {
					select {
					case chunk, ok := <-chunks:
						if !ok {
							t.Fatalf("PTY closed before %s:\n%s", marker, output.Bytes())
						}
						output.Write(chunk)
					case <-deadline.C:
						t.Fatalf("missing %s:\n%s", marker, output.Bytes())
					}
				}
			}
			waitFor("fixture-host")
			for operation := 1; operation <= 3; operation++ {
				if _, err := terminal.Write([]byte(input.trigger)); err != nil {
					t.Fatal(err)
				}
				waitFor(fmt.Sprintf("NATIVE_READY_%d", operation))
				if _, err := terminal.Write([]byte(input.data)); err != nil {
					t.Fatal(err)
				}
				if _, err := controlWrite.Write([]byte{1}); err != nil {
					t.Fatal(err)
				}
				waitFor(fmt.Sprintf("RETURNED_%d", operation))
				// A non-activating key is a deterministic input barrier. All bytes
				// written before it must belong to the native operation, not reopen it.
				if _, err := terminal.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
				waitFor(fmt.Sprintf("INPUT_BARRIER_%d", operation))
				if bytes.Contains(output.Bytes(), []byte(fmt.Sprintf("NATIVE_READY_%d", operation+1))) {
					t.Fatalf("native typeahead launched another navigation:\n%s", output.Bytes())
				}
			}
		})
	}
}

type fleetExecutionPTYModel struct {
	Model
	barriers int
}

func (m fleetExecutionPTYModel) Init() tea.Cmd { return m.Model.Init() }
func (m fleetExecutionPTYModel) View() string {
	return m.Model.View() + fmt.Sprintf("\nINPUT_BARRIER_%d\n", m.barriers)
}
func (m fleetExecutionPTYModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "x" && !m.fleetTerminalActive {
		m.barriers++
	}
	next, command := m.Model.Update(msg)
	m.Model = next.(Model)
	return m, command
}

func TestFleetExecutionPTYHelper(t *testing.T) {
	if os.Getenv(fleetExecutionPTYEnv) != "1" {
		t.Skip("helper process")
	}
	control := os.NewFile(3, "execution-control")
	defer control.Close()
	operations := 0
	actions := Actions{
		LoadFleetHosts: func(context.Context) (FleetHostsResult, error) {
			return FleetHostsResult{Hosts: []FleetHostDescriptor{{Key: "host", Name: "fixture-host", EndpointID: "endpoint", Target: "fixture"}}}, nil
		},
		NavigateFleet: func(context.Context, FleetRow) (*FleetExecution, error) {
			return &FleetExecution{Run: func(_ context.Context, input io.Reader, output, errOut io.Writer) (FleetExecutionResult, error) {
				operations++
				file := input.(interface{ Fd() uintptr })
				state, err := term.MakeRaw(file.Fd())
				if err != nil {
					return FleetExecutionResult{}, err
				}
				defer term.Restore(file.Fd(), state)
				fmt.Fprintf(output, "NATIVE_READY_%d\n", operations)
				var release [1]byte
				if _, err := io.ReadFull(control, release[:]); err != nil {
					return FleetExecutionResult{}, err
				}
				return FleetExecutionResult{Summary: fmt.Sprintf("RETURNED_%d", operations)}, nil
			}}, nil
		},
	}
	uiCtx, cancel := context.WithCancel(t.Context())
	m := New(actions, nil, nil).WithContext(uiCtx).WithFleetBackgroundRefresh(false)
	m.view = ViewFleet
	wrapper := fleetExecutionPTYModel{Model: m}
	for {
		final, err := tea.NewProgram(wrapper, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		wrapper = final.(fleetExecutionPTYModel)
		handoff := wrapper.Model.FleetHandoff()
		if handoff == nil {
			return
		}
		result, runErr := handoff.Run(t.Context(), os.Stdin, os.Stdout, os.Stderr)
		uiCtx, cancel = context.WithCancel(t.Context())
		wrapper.Model = wrapper.Model.ResumeFleetHandoff(uiCtx, result, runErr)
		if err := wrapper.Model.TerminalHandoffError(); err != nil {
			cancel()
			t.Fatal(err)
		}
	}
}
