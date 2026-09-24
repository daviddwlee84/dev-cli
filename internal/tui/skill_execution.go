package tui

import (
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
)

// Preparing an Exec message must not acquire the provider lease. Bubble Tea
// can discard that message, or fail to release its terminal before Run starts.
// MutationCommand.Run owns acquisition and release for the actual process.
type skillExecutionCommand struct {
	mutation *agentskill.MutationCommand
}

func (c *skillExecutionCommand) Run() error { return c.mutation.Run() }

// Match tea.ExecProcess: retain streams explicitly selected by the provider.
func (c *skillExecutionCommand) SetStdin(reader io.Reader) {
	if c.mutation != nil && c.mutation.Command != nil && c.mutation.Command.Stdin == nil {
		c.mutation.Command.Stdin = reader
	}
}

func (c *skillExecutionCommand) SetStdout(writer io.Writer) {
	if c.mutation != nil && c.mutation.Command != nil && c.mutation.Command.Stdout == nil {
		c.mutation.Command.Stdout = writer
	}
}

func (c *skillExecutionCommand) SetStderr(writer io.Writer) {
	if c.mutation != nil && c.mutation.Command != nil && c.mutation.Command.Stderr == nil {
		c.mutation.Command.Stderr = writer
	}
}

func runSkillMutation(mutation *agentskill.MutationCommand, complete func(error) tea.Msg) tea.Cmd {
	return tea.Exec(&skillExecutionCommand{mutation: mutation}, func(err error) tea.Msg {
		return afterExec(complete(err))
	})
}
