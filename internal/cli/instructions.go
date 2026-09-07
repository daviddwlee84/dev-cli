package cli

import "github.com/spf13/cobra"

func newInstructionsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "instructions", Short: "Share AGENTS.md and CLAUDE.md with guarded transfers"}
	cmd.AddCommand(newAgentTransferCmd(app, "instructions"))
	return cmd
}
