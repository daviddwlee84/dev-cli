package cli

import (
	"context"
	"errors"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/sshremote"
	"github.com/spf13/cobra"
)

// These are fixed internal protocols, separate from the existing local-files
// capability. They never recurse into the remote host's fleet configuration.
func newSSHRemoteHelperCmds(app *App) []*cobra.Command {
	var commands []*cobra.Command
	for _, name := range []string{"_ssh-capability", "_ssh-inventory", "_ssh-resolve", "_ssh-keys"} {
		name := name
		command := &cobra.Command{Use: name, Hidden: true, Args: cobra.NoArgs,
			// A helper may run under a PTY later; never trigger release checks or
			// notices from the ordinary interactive root pre-run.
			PersistentPreRunE: func(*cobra.Command, []string) error { return app.Load() },
			RunE: func(cmd *cobra.Command, _ []string) error {
				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				defer cancel()
				service, err := app.sshHosts()
				if err != nil {
					return writeSSHRemoteFailure(app, err)
				}
				server := sshremote.NewServer(service)
				var response any
				switch name {
				case "_ssh-capability":
					var request sshremote.CapabilityRequest
					if fleet.DecodeStrict(app.In, sshremote.MaxRequestBytes, &request) != nil {
						return writeSSHRemoteFailure(app, sshremote.ErrInvalidData)
					}
					response, err = server.Capability(ctx, request)
				case "_ssh-inventory":
					var request sshremote.Request
					if fleet.DecodeStrict(app.In, sshremote.MaxRequestBytes, &request) != nil {
						return writeSSHRemoteFailure(app, sshremote.ErrInvalidData)
					}
					response, err = server.Inventory(ctx, request)
				case "_ssh-resolve":
					var request sshremote.ResolveRequest
					if fleet.DecodeStrict(app.In, sshremote.MaxRequestBytes, &request) != nil {
						return writeSSHRemoteFailure(app, sshremote.ErrInvalidData)
					}
					response, err = server.Resolve(ctx, request)
				case "_ssh-keys":
					var request sshremote.KeysRequest
					if fleet.DecodeStrict(app.In, sshremote.MaxRequestBytes, &request) != nil {
						return writeSSHRemoteFailure(app, sshremote.ErrInvalidData)
					}
					response, err = server.Keys(ctx, request)
				}
				if err != nil {
					return writeSSHRemoteFailure(app, err)
				}
				body, encodeErr := fleet.MarshalBounded(response, sshremote.MaxResponseBytes)
				if encodeErr != nil {
					return writeSSHRemoteFailure(app, sshremote.ErrInvalidData)
				}
				_, err = app.Out.Write(body)
				return err
			}}
		commands = append(commands, command)
	}
	var encoded string
	connect := &cobra.Command{Use: "_ssh-connect", Hidden: true, Args: cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return app.Load() },
		RunE: func(cmd *cobra.Command, _ []string) error {
			request, err := sshremote.DecodeConnectRequest(encoded)
			if err != nil {
				return errors.New("invalid remote SSH connection request")
			}
			service, err := app.sshHosts()
			if err != nil {
				return err
			}
			result, err := sshremote.NewServer(service).Connect(cmd.Context(), request)
			if err != nil {
				return err
			}
			if result.ExitCode != 0 {
				code := result.ExitCode
				if code < 0 {
					code = 255
				}
				return &sshremote.ExitError{Code: code}
			}
			return nil
		}}
	connect.Flags().StringVar(&encoded, "request", "", "encoded connection request")
	_ = connect.Flags().MarkHidden("request")
	commands = append(commands, connect)
	return commands
}

func writeSSHRemoteFailure(app *App, err error) error {
	return writeFleetProtocol(app.Out, sshremote.ProtocolError(err), fleet.MaxCapabilityBytes)
}
