package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/spf13/cobra"
)

func newSSHDiagnoseCmd(app *App) *cobra.Command {
	var request sshhost.DiagnoseRequest
	var jsonOut bool
	cmd := &cobra.Command{Use: "diagnose <target>", Short: "Diagnose SSH configuration, routing, transport and authentication", Long: `Explicitly evaluate OpenSSH configuration and perform bounded network probes.
Accepts an alias, hostname or IP literal. Match exec and configured proxy/key
helpers may run. Probes use fresh noninteractive connections and strict host-key
checking without updating known_hosts or SSH/network settings. JSON contains
local endpoint information; feedback drafts use a separate public projection.`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request.Target = args[0]
		return runSSHDiagnose(cmd.Context(), app, request, jsonOut)
	}}
	cmd.Flags().BoolVar(&request.CompareQoS, "compare-qos", false, "allow one comparable fresh SSH attempt with IPQoS=none")
	cmd.Flags().DurationVar(&request.Timeout, "timeout", sshhost.DefaultDiagnoseTimeout, "total diagnostic deadline")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit one versioned local diagnosis, including partial failures")
	cmd.ValidArgsFunction = completeSSHAliases(app)
	return cmd
}
func runSSHDiagnose(ctx context.Context, app *App, request sshhost.DiagnoseRequest, jsonOut bool) error {
	if err := sshhost.ValidateDiagnosticTarget(request.Target); err != nil {
		return asUsageError(err)
	}
	if request.Timeout < 0 {
		return asUsageError(errors.New("--timeout must be positive"))
	}
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	result, err := service.Diagnose(ctx, request)
	if jsonOut {
		if writeErr := writeSSHJSON(app, result); writeErr != nil {
			return writeErr
		}
	} else {
		fmt.Fprintf(app.Out, "SSH diagnosis: %s\n", result.Status)
		if result.Target.Hostname != "" {
			fmt.Fprintf(app.Out, "Target: %s:%d  user: %s  proxy: %s\n", result.Target.Hostname, result.Target.Port, result.Target.User, result.Target.Proxy)
		}
		for _, stage := range result.Stages {
			fmt.Fprintf(app.Out, "%-15s %-12s %s\n", stage.Name, stage.State, stage.Code)
		}
		for _, attempt := range result.Attempts {
			fmt.Fprintf(app.Out, "%s: %s\n", attempt.Name, attempt.Code)
			for _, stage := range attempt.Stages {
				fmt.Fprintf(app.Out, "  %-13s %-12s %s\n", stage.Name, stage.State, stage.Code)
			}
		}
		for _, action := range result.SuggestedActions {
			fmt.Fprintln(app.Out, "Next: "+action)
		}
		if len(result.Findings) > 0 {
			fmt.Fprintln(app.Out, "QoS comparison shows correlation only; it does not locate a faulty network component.")
		}
	}
	return err
}
