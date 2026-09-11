package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

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
		if result.Target.IPQoS != "" {
			fmt.Fprintf(app.Out, "IPQoS: %s  address family: %s\n", result.Target.IPQoS, result.Target.AddressFamily)
		}
		if len(result.Target.IdentityFiles) > 0 {
			fmt.Fprintln(app.Out, "Identity files: "+strings.Join(result.Target.IdentityFiles, ", "))
		}
		for _, route := range result.Routes {
			fmt.Fprintf(app.Out, "Route %s: interface=%s source=%s gateway=%s (%s)\n", route.Address, route.Interface, route.Source, route.Gateway, route.Code)
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
			fmt.Fprintln(app.Out, "Next: "+sshDiagnosticActionText(action))
		}
		for _, finding := range result.Findings {
			if finding == "qos_correlated_progress" {
				fmt.Fprintln(app.Out, "QoS comparison shows correlation only; it does not locate a faulty network component.")
			}
			if finding == "tcp_ssh_endpoints_differ" {
				fmt.Fprintln(app.Out, "The TCP socket and SSH attempt selected different endpoints; their results are not a controlled comparison.")
			}
		}
	}
	return err
}

func sshDiagnosticActionText(action string) string {
	switch action {
	case "verify_host_identity":
		return "Verify the host identity through a trusted channel; keep strict host-key checking enabled."
	case "check_authentication":
		return "Check the effective user, selected identities and agent availability; this probe does not prompt for credentials."
	case "check_remote_session":
		return "Client logs report authentication; inspect the remote shell or session policy. A failed command does not prove a completed login."
	case "inspect_ssh_config":
		return "Inspect the target with ssh -G and correct its local configuration."
	case "review_per_host_ipqos":
		return "Review a per-host IPQoS workaround using the comparison evidence; no configuration was changed."
	default:
		return "Inspect the route, proxy and service port; --compare-qos explicitly permits an eligible QoS comparison."
	}
}
