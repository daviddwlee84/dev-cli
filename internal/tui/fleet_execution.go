package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// FleetExecution owns a terminal operation and reports its result directly.
// Completion can refresh observations, but never implicitly opens another view
// or starts another navigation operation.
type FleetExecution struct {
	Run func(context.Context, io.Reader, io.Writer, io.Writer) (FleetExecutionResult, error)
}

type FleetExecutionResult struct {
	Summary                   string
	RefreshHerdr, RefreshHost bool
	Directory, RuntimeHandle  string
}

func (m Model) TerminalHandoffError() error { return m.terminalHandoffErr }

// FleetHandoff runs after the old Program has fully stopped, ending both its
// raw reader and any sender holding already-decoded input events.
type FleetHandoff struct {
	host        FleetHostDescriptor
	action      string
	execution   *FleetExecution
	boundaryErr error
	input       io.Reader
}

func (h *FleetHandoff) Run(ctx context.Context, in io.Reader, out, errOut io.Writer) (FleetExecutionResult, error) {
	command := &fleetExecutionCommand{execution: h.execution, ctx: ctx, in: in, out: out, errOut: errOut}
	err := command.Run()
	h.boundaryErr = command.boundaryErr
	h.input = in
	return command.result, err
}

func (m Model) FleetHandoff() *FleetHandoff { return m.pendingFleetHandoff }

type fleetExecutionCommand struct {
	execution   *FleetExecution
	ctx         context.Context
	in          io.Reader
	out, errOut io.Writer
	result      FleetExecutionResult
	boundaryErr error
}

func (c *fleetExecutionCommand) SetStdin(in io.Reader)   { c.in = in }
func (c *fleetExecutionCommand) SetStdout(out io.Writer) { c.out = out }
func (c *fleetExecutionCommand) SetStderr(out io.Writer) { c.errOut = out }
func (c *fleetExecutionCommand) Run() error {
	if err := flushTerminalInput(c.in); err != nil {
		c.boundaryErr = fmt.Errorf("release dashboard input: %w", err)
		return c.boundaryErr
	}
	result, err := c.execution.Run(c.ctx, c.in, c.out, c.errOut)
	c.result = result
	if resetErr := flushTerminalInput(c.in); resetErr != nil {
		c.boundaryErr = fmt.Errorf("restore dashboard input: %w", resetErr)
	}
	return errors.Join(err, c.boundaryErr)
}

type fleetExecutionReadyMsg struct {
	host      FleetHostDescriptor
	action    string
	execution *FleetExecution
	err       error
}
