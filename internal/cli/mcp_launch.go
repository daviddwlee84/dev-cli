package cli

import (
	"github.com/daviddwlee84/dev-cli/internal/agentinterop"
	"github.com/spf13/cobra"
)

func newMCPLaunchCmd(app *App) *cobra.Command {
	var binding, state string
	cmd := &cobra.Command{Use: "_launch", Hidden: true, Args: cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			return (agentinterop.Service{StateDir: state}).Launch(cmd.Context(), binding)
		},
	}
	cmd.Flags().StringVar(&binding, "binding", "", "private applied binding ID")
	cmd.Flags().StringVar(&state, "state", "", "private interop state directory")
	_ = cmd.MarkFlagRequired("binding")
	_ = cmd.MarkFlagRequired("state")
	return cmd
}
