package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/localfiles"
	"github.com/daviddwlee84/dev-cli/internal/machineid"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/spf13/cobra"
)

type fleetMachineIDReport struct {
	SchemaVersion       int     `json:"schema_version"`
	Host                string  `json:"host"`
	MachineID           string  `json:"machine_id"`
	ConfiguredMachineID *string `json:"configured_machine_id"`
	PinState            string  `json:"pin_state"`
	Platform            string  `json:"platform"`
	Feature             string  `json:"feature"`
	ProtocolVersion     int     `json:"protocol_version"`
	Supported           bool    `json:"supported"`
	Reason              *string `json:"reason"`
}

func newFleetMachineIDCmd(app *App) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "machine-id <host>",
		Short: "Show and compare a remote host's stable machine ID",
		Long: `Run the content-free capability probe for one configured fleet host and
compare its stable machine UUID with the optional machine_id pin in remotes.toml.
This command never writes the pin; verify the UUID through an independent channel
before copying it into host configuration.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fleetConfig, err := loadFleetConfig(app)
			if err != nil {
				return err
			}
			host, err := fleetFilesHost(fleetConfig, args[0])
			if err != nil {
				return err
			}
			client := localfiles.Client{Runner: fleet.Transport{
				Err: app.Err, StdinLimit: fleet.MaxCapabilityBytes, StdoutLimit: fleet.MaxCapabilityBytes,
			}}
			capability, err := client.Identify(ctxOf(), host)
			if err != nil {
				return err
			}
			report := fleetMachineIDReport{
				SchemaVersion: 1, Host: host.Name, MachineID: capability.MachineID,
				PinState: "unpinned", Platform: capability.Platform, Feature: capability.Feature,
				ProtocolVersion: capability.ProtocolVersion, Supported: capability.Supported,
			}
			if host.MachineID != "" {
				configured := host.MachineID
				report.ConfiguredMachineID = &configured
				report.PinState = "match"
				if configured != capability.MachineID {
					report.PinState = "mismatch"
				}
			}
			if capability.Reason != "" {
				reason := capability.Reason
				report.Reason = &reason
			}
			if jsonOutput {
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(report)
			}
			featureState := "supported"
			if !report.Supported {
				featureState = "unsupported"
				if report.Reason != nil {
					featureState += " (" + *report.Reason + ")"
				}
			}
			table := app.newTable("HOST", "MACHINE ID", "PIN", "PLATFORM", "LOCAL FILES")
			table.Add(report.Host, report.MachineID, report.PinState, report.Platform, featureState)
			table.Render(app.Out)
			if report.PinState != "match" {
				fmt.Fprintf(app.Out, "\nVerify the UUID independently, then set machine_id = %q for host %q in %s.\n",
					report.MachineID, report.Host, config.Contract(fleetConfigPath(app)))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return cmd
}

func newFleetFilesCmd(app *App) *cobra.Command {
	var targets []string
	var filePatterns []string
	var apply, replace, yes, jsonOutput bool
	cmd := &cobra.Command{
		Use:   "files [repo-or-path]",
		Short: "Plan or apply one-way transfer of explicit ignored local files",
		Long: `Expand project [local_files] patterns and repeatable --file patterns in one
selected checkout, then prove each exact file is untracked, ignored, regular, and
portable on both hosts. The target clone, branch, and HEAD must already match.
Report-only is the default; differing target content requires explicit --replace.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(targets) != 1 || strings.TrimSpace(targets[0]) == "" {
				return errors.New("fleet files requires exactly one --to host")
			}
			target := targets[0]
			fleetConfig, err := loadFleetConfig(app)
			if err != nil {
				return err
			}
			host, err := fleetFilesHost(fleetConfig, target)
			if err != nil {
				return err
			}
			transport := fleet.Transport{
				Err: app.Err, StdinLimit: localfiles.MaxApplyEnvelopeBytes,
				StdoutLimit: localfiles.MaxApplyResponseBytes,
			}
			client := localfiles.Client{Runner: transport}
			// Capability is deliberately first: no project file body or source file
			// bytes have been read when an old, unpinned, or unsupported target fails.
			capability, err := client.Probe(ctxOf(), host)
			if err != nil {
				return err
			}
			if runtime.GOOS == "windows" {
				return errors.New("native Windows local-file payloads are not enabled")
			}
			if apply && host.MachineID == "" {
				return localfiles.ErrMachineUnpinned
			}
			limits, err := localfiles.NegotiateLimits(app.Cfg.LocalFiles.Limits(), capability.Limits)
			if err != nil {
				return err
			}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			checkout, status, head, identity, err := resolveFleetFilesSource(ctxOf(), app, ref)
			if err != nil {
				return err
			}
			project, err := projectconfig.Load(checkout, nil)
			if err != nil {
				return err
			}
			patterns := fleetFilePatterns(project, filePatterns)
			sourceMachine, err := machineid.LoadOrCreate(ctxOf())
			if err != nil {
				return err
			}
			source, sourceReport, err := localfiles.PrepareSource(ctxOf(), localfiles.SourceOptions{
				Checkout: checkout,
				Binding: localfiles.Binding{
					RemoteIdentity: identity, Branch: status.Branch, HeadOID: head,
					SourceMachine: sourceMachine, TargetMachine: capability.MachineID,
				},
				Patterns: patterns, Limits: limits, Replace: replace,
			})
			if err != nil {
				if len(sourceReport.Files) > 0 {
					if renderErr := renderFleetFilesReport(app, sourceReport, jsonOutput); renderErr != nil {
						return renderErr
					}
				}
				return err
			}
			plan, err := client.Plan(ctxOf(), host, capability, source.Request())
			if err != nil {
				return err
			}
			planReport := plan.PublicReport()
			if !planReport.Successful() {
				if err := renderFleetFilesReport(app, planReport, jsonOutput); err != nil {
					return err
				}
				return localfiles.ErrPlanBlocked
			}
			if !apply {
				return renderFleetFilesReport(app, planReport, jsonOutput)
			}
			if jsonOutput && !yes {
				return errors.New("fleet files --apply --json requires --yes")
			}
			if !yes {
				if !app.interactive() {
					return errors.New("non-interactive fleet files --apply requires --yes")
				}
				if err := renderFleetFilesReport(app, planReport, false); err != nil {
					return err
				}
				if !confirm(app, bufio.NewReader(app.In), fmt.Sprintf("apply %d portable file(s) to %s", len(planReport.Files), host.Name)) {
					fmt.Fprintln(app.Out, "Cancelled; no files changed.")
					return nil
				}
			}
			envelope, err := source.BuildEnvelope(ctxOf(), plan, false)
			if err != nil {
				return err
			}
			result, err := client.Apply(ctxOf(), host, capability, envelope)
			if err != nil {
				return err
			}
			return renderFleetFilesReport(app, result.PublicReport(), jsonOutput)
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&targets, "to", nil, "single configured target host (required)")
	flags.StringArrayVar(&filePatterns, "file", nil, "additional portable relative pattern (repeatable)")
	flags.BoolVar(&apply, "apply", false, "apply the verified plan")
	flags.BoolVar(&replace, "replace", false, "replace differing target files observed by this plan")
	flags.BoolVar(&yes, "yes", false, "confirm apply without prompting (does not imply --replace)")
	flags.BoolVar(&jsonOutput, "json", false, "emit content-free JSON")
	cmd.ValidArgsFunction = completeRepos(app)
	return cmd
}

func fleetFilesHost(cfg fleet.Config, name string) (fleet.Host, error) {
	var selected *fleet.Host
	for index := range cfg.Hosts {
		if cfg.Hosts[index].Name == name {
			if selected != nil {
				return fleet.Host{}, errors.New("fleet files requires exactly one target")
			}
			copy := cfg.Hosts[index]
			selected = &copy
		}
	}
	if selected == nil {
		return fleet.Host{}, fmt.Errorf("unknown fleet host %q", name)
	}
	return *selected, nil
}

func fleetFilePatterns(project projectconfig.Result, adHoc []string) []localfiles.Pattern {
	var patterns []localfiles.Pattern
	if project.Effective.LocalFiles.Include != nil {
		source, _ := project.SourceFor("local_files.include")
		for _, value := range *project.Effective.LocalFiles.Include {
			patterns = append(patterns, localfiles.Pattern{Value: value, Source: source})
		}
	}
	for _, value := range adHoc {
		patterns = append(patterns, localfiles.Pattern{Value: value, Source: "--file"})
	}
	return patterns
}

func resolveFleetFilesSource(ctx context.Context, app *App, ref string) (string, gitx.Status, string, string, error) {
	checkout := ""
	if ref == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", gitx.Status{}, "", "", err
		}
		gitRepo, err := gitx.Discover(ctx, cwd)
		if err != nil || gitRepo.Bare {
			return "", gitx.Status{}, "", "", errors.New("fleet files requires a non-bare Git checkout")
		}
		checkout = gitRepo.Root
	} else if absolute, err := filepath.Abs(ref); err == nil {
		if _, statErr := os.Stat(absolute); statErr == nil {
			gitRepo, discoverErr := gitx.Discover(ctx, absolute)
			if discoverErr != nil || gitRepo.Bare {
				return "", gitx.Status{}, "", "", errors.New("explicit fleet files path is not inside a non-bare Git checkout")
			}
			checkout = gitRepo.Root
		}
	}
	if checkout == "" {
		repository, _, err := repo.Resolve(ctx, app.Cfg.DiscoveryRoots(), ref)
		if err != nil {
			return "", gitx.Status{}, "", "", err
		}
		gitRepo, err := gitx.Discover(ctx, repository.Path)
		if err != nil || gitRepo.Bare {
			return "", gitx.Status{}, "", "", errors.New("fleet files requires a non-bare canonical checkout")
		}
		checkout = gitRepo.MainRoot
	}
	status, err := gitx.StatusOf(ctx, checkout)
	if err != nil {
		return "", gitx.Status{}, "", "", err
	}
	if status.Detached || status.Branch == "" {
		return "", gitx.Status{}, "", "", errors.New("fleet files requires an attached source branch")
	}
	head, err := gitx.Run(ctx, checkout, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", gitx.Status{}, "", "", err
	}
	identity, err := localfiles.FetchRemoteIdentity(ctx, checkout, status.Branch)
	if err != nil {
		return "", gitx.Status{}, "", "", err
	}
	return checkout, status, head, identity, nil
}

func renderFleetFilesReport(app *App, report localfiles.Report, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(app.Out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	table := app.newTable("PATH", "SIZE", "MODE", "STATE")
	for _, file := range report.Files {
		table.Add(file.Path, fmt.Sprintf("%d", file.Size), file.Mode, string(file.State))
	}
	table.Render(app.Out)
	return nil
}

func writeFleetProtocol(writer io.Writer, value any, limit int64) error {
	body, err := fleet.MarshalBounded(value, limit)
	if err != nil {
		return err
	}
	_, err = writer.Write(body)
	return err
}

func newFleetCapabilityProtocolCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "_capability", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var request fleet.CapabilityRequest
			if err := fleet.DecodeStrict(app.In, fleet.MaxCapabilityBytes, &request); err != nil {
				return err
			}
			response, err := localfiles.NewService(app.Cfg).Capability(ctxOf(), request)
			if err != nil {
				return err
			}
			return writeFleetProtocol(app.Out, response, fleet.MaxCapabilityBytes)
		},
	}
}

func newFleetFilesPlanProtocolCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "_files-plan", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var request localfiles.PlanRequest
			if err := fleet.DecodeStrict(app.In, localfiles.MaxPlanEnvelopeBytes, &request); err != nil {
				return err
			}
			response, err := localfiles.NewService(app.Cfg).Plan(ctxOf(), request)
			wire := localfiles.PlanWireResponse{
				SchemaVersion: localfiles.SchemaVersion, ProtocolVersion: fleet.LocalFilesProtocolVersion,
			}
			if err != nil {
				var target *localfiles.TargetError
				if !errors.As(err, &target) {
					return err
				}
				wire.ErrorCode = target.Code
			} else {
				wire.Plan = &response
			}
			if err := wire.Validate(request); err != nil {
				return err
			}
			return writeFleetProtocol(app.Out, wire, localfiles.MaxPlanEnvelopeBytes)
		},
	}
}

func newFleetFilesApplyProtocolCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "_files-apply", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			service := localfiles.NewService(app.Cfg)
			// Do not consume stdin on unsupported native Windows targets. A valid
			// capability exchange is the only route by which a client sends content.
			capability, err := service.Capability(ctxOf(), fleet.LocalFilesCapabilityRequest())
			if err != nil {
				return err
			}
			if !capability.Supported {
				return errors.New("native target does not support local-file payloads")
			}
			var envelope localfiles.ApplyEnvelope
			if err := fleet.DecodeStrict(app.In, localfiles.MaxApplyEnvelopeBytes, &envelope); err != nil {
				return err
			}
			response, err := service.Apply(ctxOf(), envelope)
			if err != nil {
				return err
			}
			return writeFleetProtocol(app.Out, response, localfiles.MaxApplyResponseBytes)
		},
	}
}
