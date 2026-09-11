package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/dotfile"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/spf13/cobra"
)

type fleetDotfileResult struct {
	Host   string          `json:"host"`
	State  fleet.HostState `json:"state"`
	Status *dotfile.Status `json:"status,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func newFleetDotfileCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use: "dotfile", Short: "Inspect host-local dotfiles across configured fleet hosts",
		PersistentPreRunE: dotfilePreRun(app),
	}
	var hosts []string
	var jsonOutput bool
	status := &cobra.Command{
		Use: "status", Short: "Read static chezmoi configuration and revision from fleet hosts",
		Long: "Use each explicitly selected host's own default chezmoi configuration. This does not run chezmoi, evaluate deployment drift, fetch Git refs, or apply dotfiles. At least one --host is required.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(hosts) == 0 {
				return errors.New("fleet dotfile status requires at least one explicit --host")
			}
			cfg, err := loadFleetConfig(app)
			if err != nil {
				return err
			}
			results, err := collectFleetDotfileStatus(cmd.Context(), cfg, hosts)
			if err != nil {
				return err
			}
			return renderFleetDotfileResults(app, results, jsonOutput)
		},
	}
	status.Flags().StringSliceVar(&hosts, "host", nil, "only these configured host names (repeatable)")
	status.Flags().BoolVar(&jsonOutput, "json", false, "emit content-free status JSON")
	_ = status.MarkFlagRequired("host")
	cmd.AddCommand(status)
	return cmd
}

func renderFleetDotfileResults(app *App, results []fleetDotfileResult, jsonOutput bool) error {
	if jsonOutput {
		payload := struct {
			SchemaVersion int                  `json:"schema_version"`
			Hosts         []fleetDotfileResult `json:"hosts"`
		}{dotfile.SchemaVersion, results}
		if err := json.NewEncoder(app.Out).Encode(payload); err != nil {
			return err
		}
	} else {
		table := app.newTable("HOST", "STATE", "CHEZMOI", "CONFIG", "SOURCE", "REVISION", "DEPLOYMENT")
		for _, result := range results {
			if result.Status == nil {
				table.Add(result.Host, string(result.State), "—", "unknown", "unknown", "—", "unknown")
				continue
			}
			s := result.Status
			installed := "absent"
			if s.Installed {
				installed = "present"
			}
			table.Add(result.Host, string(result.State), installed, s.ConfigState, s.SourceState, dash(s.GitRevision), "unknown")
		}
		table.Render(app.Out)
		fmt.Fprintln(app.Out, "Deployment drift is not evaluated; each host retains its own source and configuration.")
	}
	for _, result := range results {
		if result.State != fleet.HostOK {
			return errors.New("some fleet dotfile observations are unavailable")
		}
	}
	return nil
}

func newFleetDotfileStatusHelperCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "_dotfile-status", Hidden: true, Args: cobra.NoArgs,
		PersistentPreRunE: dotfilePreRun(app),
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := fleet.MarshalBounded(dotfile.Observe(cmd.Context(), dotfile.Options{}), dotfile.MaxStatusBytes)
			if err != nil {
				return err
			}
			_, err = app.Out.Write(data)
			return err
		},
	}
}

func collectFleetDotfileStatus(ctx context.Context, cfg fleet.Config, names []string) ([]fleetDotfileResult, error) {
	if len(names) == 0 {
		return nil, errors.New("fleet dotfile status requires at least one explicit --host")
	}
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		selected[name] = true
	}
	found := map[string]bool{}
	for _, host := range cfg.Hosts {
		found[host.Name] = true
	}
	for name := range selected {
		if !found[name] {
			return nil, fmt.Errorf("unknown fleet host %q", name)
		}
	}
	results := []fleetDotfileResult{}
	var targets []fleet.Host
	for _, host := range cfg.Hosts {
		if selected[host.Name] {
			targets = append(targets, host)
		}
	}
	remote := make([]fleetDotfileResult, len(targets))
	parallel := cfg.Defaults.MaxParallel
	if parallel < 1 {
		parallel = 4
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for index, host := range targets {
		wg.Add(1)
		go func(index int, host fleet.Host) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				remote[index] = fleetDotfileResult{Host: host.Name, State: fleet.HostTimeout, Error: "observation-canceled"}
				return
			}
			remote[index] = collectFleetDotfileHost(ctx, host, fleet.Transport{StdoutLimit: dotfile.MaxStatusBytes})
		}(index, host)
	}
	wg.Wait()
	return append(results, remote...), nil
}

type dotfileFleetRunner interface {
	RunWithOptions(context.Context, fleet.Host, []string, []byte, fleet.RunOptions) fleet.Result
}

type dotfileFleetRunFunc func(context.Context, fleet.Host, []string, fleet.RunOptions) fleet.Result

func (f dotfileFleetRunFunc) RunWithOptions(ctx context.Context, host fleet.Host, args []string, _ []byte, options fleet.RunOptions) fleet.Result {
	return f(ctx, host, args, options)
}

func collectFleetDotfileHost(ctx context.Context, host fleet.Host, runner dotfileFleetRunner) fleetDotfileResult {
	result := fleetDotfileResult{Host: host.Name, State: fleet.HostOK}
	response := runner.RunWithOptions(ctx, host, []string{"fleet", "_dotfile-status"}, nil, fleet.RunOptions{Retry: fleet.RetryAuthentication})
	switch {
	case response.TimedOut:
		result.State, result.Error = fleet.HostTimeout, "observation-timeout"
	case response.CaptureError != "":
		result.State, result.Error = fleet.HostInvalid, "invalid-response"
	case response.ExitCode == 127:
		result.State, result.Error = fleet.HostNoDev, "dev-not-installed"
	case response.ExitCode == 255:
		result.State, result.Error = fleet.HostUnreachable, "host-unreachable"
	case response.ExitCode != 0:
		result.State, result.Error = fleet.HostIncompatible, "dotfile-status-unavailable"
	default:
		var status dotfile.Status
		var required struct {
			Installed *bool `json:"installed"`
		}
		if err := fleet.UnmarshalStrict(response.Stdout, dotfile.MaxStatusBytes, &status); err != nil || json.Unmarshal(response.Stdout, &required) != nil || required.Installed == nil || !validDotfileStatus(status) {
			result.State, result.Error = fleet.HostInvalid, "invalid-response"
		} else {
			result.Status = &status
		}
	}
	return result
}

func validDotfileStatus(status dotfile.Status) bool {
	validState := func(state string) bool { return state == "present" || state == "absent" || state == "unknown" }
	if status.SchemaVersion != dotfile.SchemaVersion || status.Platform == "" || len(status.Platform) > 32 || !validState(status.ConfigState) || !validState(status.SourceState) || status.DeploymentDrift != "unknown" {
		return false
	}
	for _, c := range status.Platform {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	switch status.GitState {
	case "present":
		if status.SourceState != "present" || len(status.GitRevision) != 40 && len(status.GitRevision) != 64 {
			return false
		}
		for _, c := range status.GitRevision {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	case "unknown", "absent", "unavailable", "unborn":
		if status.GitRevision != "" || status.GitBranch != "" {
			return false
		}
	default:
		return false
	}
	return !strings.ContainsAny(status.GitBranch, "\x00\r\n")
}
