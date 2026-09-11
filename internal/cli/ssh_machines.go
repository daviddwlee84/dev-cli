package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/spf13/cobra"
)

func sshDiscoveryCacheDir() string { return filepath.Join(cacheRoot(), "ssh-discovery") }

type sshDiscoveryReadOnlyKey struct{}

func withSSHMachineJSON(app *App, jsonOut bool, kind string, operation func(*App) error) error {
	if !jsonOut {
		return operation(app)
	}
	var output bytes.Buffer
	copy := *app
	copy.Out = &output
	err := operation(&copy)
	if output.Len() > 0 {
		decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
		var value any
		if e := decoder.Decode(&value); e != nil {
			return errors.New("invalid machine operation output")
		}
		var trailing any
		if e := decoder.Decode(&trailing); e != io.EOF {
			return errors.New("machine operation emitted multiple JSON documents")
		}
		if _, e := app.Out.Write(output.Bytes()); e != nil {
			return e
		}
		return err
	}
	var usage *usageError
	if errors.As(err, &usage) {
		return err
	}
	code := "operation_failed"
	if err == nil {
		code = "ready"
	}
	if e := writeSSHJSON(app, struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Status        string `json:"status"`
		ErrorCode     string `json:"error_code,omitempty"`
	}{1, kind, code, sshErrorCode(err)}); e != nil {
		return e
	}
	return err
}
func (a *App) machineStore() *machineregistry.Store {
	return machineregistry.NewStore(filepath.Join(a.Cfg.StateDir(), "machines", "registry.db"))
}
func (a *App) sshDiscovery() *sshdiscovery.Service {
	if a.sshDiscoveryService != nil {
		return a.sshDiscoveryService
	}
	return sshdiscovery.NewService(a.sshHostRunner)
}

func loadSSHMachineInventory(ctx context.Context, app *App, liveTailscale, includeLAN bool) (sshflow.MachineInventory, error) {
	management, err := app.sshManagement()
	if err != nil {
		return sshflow.MachineInventory{}, err
	}
	local, err := management.List(ctx)
	if err != nil {
		return sshflow.MachineInventory{}, err
	}
	hints, err := management.SSH.ConnectionHints(ctx)
	if err != nil {
		return sshflow.MachineInventory{}, err
	}
	registry, err := app.machineStore().Read(ctx)
	if err != nil {
		return sshflow.MachineInventory{}, err
	}
	reports, cacheErr := sshdiscovery.ReadCache(ctx, sshDiscoveryCacheDir(), time.Now())
	selected := []sshdiscovery.Report{}
	for _, report := range reports {
		if report.Source == "tailscale" && !liveTailscale || report.Source == "lan" && includeLAN {
			selected = append(selected, report)
		}
	}
	if liveTailscale {
		report, _ := app.sshDiscovery().Tailscale(ctx)
		selected = append(selected, report)
		if report.Complete {
			if err := sshdiscovery.WriteCache(ctx, sshDiscoveryCacheDir(), report); err != nil {
				app.warnf("discovery cache: %v", err)
			}
		}
	}
	out := sshflow.JoinMachines(local, hints, registry, selected, fleetConfigPath(app), filepath.Dir(local.Root), time.Now())
	if cacheErr != nil {
		out.Sources["cache"] = "unavailable"
		out.Complete = false
	}
	return out, nil
}

func runSSHCombinedList(ctx context.Context, app *App, liveTailscale, lan, jsonOut bool, format string) error {
	inv, err := loadSSHMachineInventory(ctx, app, liveTailscale, lan)
	if err != nil {
		return err
	}
	if jsonOut {
		var buf bytes.Buffer
		copy := *app
		copy.Out = &buf
		if err := runSSHList(ctx, &copy, true, ""); err != nil {
			return err
		}
		var original sshListDocument
		if err := json.Unmarshal(buf.Bytes(), &original); err != nil {
			return err
		}
		original.Complete = original.Complete && inv.Complete
		return writeSSHJSON(app, struct {
			sshListDocument
			Machines   []sshflow.MachineRow `json:"machines"`
			Sources    map[string]string    `json:"sources"`
			ObservedAt time.Time            `json:"observed_at"`
		}{original, inv.Machines, inv.Sources, inv.ObservedAt})
	}
	if format == "tsv" {
		for _, row := range inv.Machines {
			fmt.Fprintf(app.Out, "%s\t%s\t%s\t%s\n", row.ID, row.Label, row.State, strings.Join(machineAliases(row), ","))
		}
		return nil
	}
	renderSSHMachineInventory(app, inv)
	return nil
}

func machineAliases(row sshflow.MachineRow) []string {
	out := []string{}
	for _, p := range row.Profiles {
		out = append(out, p.Alias)
	}
	return out
}
func renderSSHMachineInventory(app *App, inv sshflow.MachineInventory) {
	table := app.newTable("MACHINE", "SSH ALIASES", "TAILSCALE", "LAN", "FLEET", "HERDR", "STATE")
	for _, row := range inv.Machines {
		ts := []string{}
		for _, c := range row.Tailscale {
			state := c.Name
			if c.Online != nil && !*c.Online {
				state += " (offline)"
			}
			if c.AdvertisesSSH {
				state += " [SSH advertised]"
			}
			ts = append(ts, state)
		}
		lan := []string{}
		for _, c := range row.LAN {
			lan = append(lan, strings.Join(c.Addresses, ",")+fmt.Sprintf(":%d", c.Port))
		}
		fleet := []string{}
		for _, p := range row.Fleet {
			fleet = append(fleet, p.Name)
		}
		herdr := []string{}
		for _, p := range row.Herdr {
			label := p.Label + "/" + p.Session
			if !p.Enabled {
				label += " (disabled)"
			}
			herdr = append(herdr, label)
		}
		table.Add(row.Label, dash(strings.Join(machineAliases(row), ", ")), dash(strings.Join(ts, ", ")), dash(strings.Join(lan, ", ")), dash(strings.Join(fleet, ", ")), dash(strings.Join(herdr, ", ")), row.State)
	}
	table.Render(app.Out)
	fmt.Fprintln(app.Out, "\nSSH aliases retain their own user, port and key. Device names suggest associations; they do not prove identity.")
	for _, source := range []string{"ssh", "tailscale", "lan", "fleet", "herdr", "cache"} {
		if state, ok := inv.Sources[source]; ok && state != "ready" {
			fmt.Fprintf(app.Out, "  %s: %s\n", source, state)
		}
	}
}

func newSSHDiscoverCmd(app *App) *cobra.Command {
	var source, iface string
	var cidrs []string
	var hosts []string
	var ports []int
	var refresh, jsonOut bool
	cmd := &cobra.Command{Use: "discover", Short: "Explicitly discover Tailscale, LAN or selected fleet SSH profiles", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if source != "tailscale" && source != "lan" && source != "fleet" {
			return asUsageError(errors.New("--source must be tailscale, lan or fleet"))
		}
		if source != "lan" && (len(cidrs) > 0 || iface != "" || cmd.Flags().Changed("ports")) {
			return asUsageError(errors.New("LAN scope flags require --source lan"))
		}
		if len(hosts) > 0 && source != "fleet" {
			return asUsageError(errors.New("--host requires --source fleet"))
		}
		if source == "fleet" {
			return runSSHFleetDiscover(cmd.Context(), app, hosts, refresh, jsonOut)
		}
		report, err := runSSHDiscovery(cmd.Context(), app, source, sshdiscovery.LANRequest{Interface: iface, Ranges: cidrs, Ports: ports}, refresh, jsonOut)
		if jsonOut {
			if e := writeSSHJSON(app, struct {
				SchemaVersion int    `json:"schema_version"`
				Kind          string `json:"kind"`
				sshdiscovery.Report
			}{1, "ssh_discovery", report}); e != nil {
				return e
			}
		} else {
			renderSSHDiscovery(app, report)
		}
		return err
	}}
	f := cmd.Flags()
	f.StringVar(&source, "source", "", "discovery source: tailscale, lan or fleet")
	f.StringArrayVar(&hosts, "host", nil, "explicit fleet source name (repeatable; never recursively explores fleets)")
	f.StringVar(&iface, "interface", "", "selected local LAN interface")
	f.StringArrayVar(&cidrs, "cidr", nil, "on-link IPv4 range (repeatable, at most 256 addresses total)")
	f.IntSliceVar(&ports, "ports", []int{22}, "SSH ports to inspect (at most 16)")
	f.BoolVar(&refresh, "refresh", false, "ignore a fresh matching LAN or fleet cache")
	f.BoolVar(&jsonOut, "json", false, "emit one versioned discovery report")
	registerFlagCompletion(cmd, "source", fixedCompletions("tailscale", "lan", "fleet"))
	return cmd
}

func runSSHDiscovery(ctx context.Context, app *App, source string, request sshdiscovery.LANRequest, refresh, jsonOut bool) (sshdiscovery.Report, error) {
	s := app.sshDiscovery()
	report := sshdiscovery.Report{Source: source, Status: sshdiscovery.StatusFailed, ObservedAt: nowSSH(), Candidates: []sshdiscovery.Candidate{}}
	var err error
	if source == "tailscale" {
		report, err = s.Tailscale(ctx)
	} else {
		if len(request.Ranges) == 0 {
			if jsonOut || !app.interactive() {
				return report, errors.New("LAN discovery requires --cidr and a selected on-link interface")
			}
			scopes, e := s.Interfaces()
			if e != nil {
				return report, e
			}
			items := []picker.Item{}
			for _, scope := range scopes {
				items = append(items, picker.Item{Value: scope.Interface + "\n" + scope.Prefix, Label: scope.Interface + " " + scope.Prefix, Description: scope.Address})
			}
			if len(items) == 0 {
				return report, errors.New("no on-link IPv4 interface is available")
			}
			picked, e := sshPick(ctx, app, "Select the LAN scope (no other interfaces will be scanned)", items, false)
			if e != nil {
				return report, e
			}
			parts := strings.SplitN(picked[0].Value, "\n", 2)
			request.Interface = parts[0]
			request.Ranges = []string{parts[1]}
			value, e := newPrompter(app).line("IPv4 CIDR (maximum 256 addresses)", parts[1])
			if e != nil {
				return report, e
			}
			request.Ranges = []string{value}
		}
		scope, e := s.LANScope(request)
		if e != nil {
			return report, e
		}
		if !refresh {
			cached, _ := sshdiscovery.ReadCache(ctx, sshDiscoveryCacheDir(), time.Now())
			for _, entry := range cached {
				if entry.Source == "lan" && entry.Scope == scope && !entry.Stale {
					return entry, nil
				}
			}
		}
		if !jsonOut {
			fmt.Fprintf(app.Out, "Discover LAN %s, ports %v (30s maximum; no authentication)\n", strings.Join(request.Ranges, ","), request.Ports)
		}
		report, err = s.LAN(ctx, request)
	}
	if (len(report.Candidates) > 0 || report.Complete) && ctx.Value(sshDiscoveryReadOnlyKey{}) != true {
		if cacheErr := sshdiscovery.WriteCache(ctx, sshDiscoveryCacheDir(), report); cacheErr != nil {
			app.warnf("discovery cache: %v", cacheErr)
		}
	}
	return report, err
}

func renderSSHDiscovery(app *App, report sshdiscovery.Report) {
	fmt.Fprintf(app.Out, "%s discovery: %s (observed %s; stale=%t)\n", report.Source, report.Status, report.ObservedAt.Format(time.RFC3339), report.Stale)
	table := app.newTable("SELECTOR", "NAME", "ADDRESS", "OS", "STATE", "TAILSCALE SSH")
	for _, c := range report.Candidates {
		advertised := "not advertised"
		if c.AdvertisesSSH {
			advertised = "advertised"
		}
		table.Add(c.ID, c.Name, strings.Join(c.Addresses, ","), c.OS, c.State, advertised)
	}
	table.Render(app.Out)
}

func newSSHMachineCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "machine", Short: "Manage canonical machine identities and source bindings"}
	show := &cobra.Command{Use: "show [machine-id]", Short: "Read the durable machine registry without creating it", Args: cobra.MaximumNArgs(1)}
	var showJSON bool
	show.Flags().BoolVar(&showJSON, "json", false, "emit the registry snapshot")
	runShow := func(app *App, cmd *cobra.Command, args []string) error {
		snapshot, err := app.machineStore().Read(cmd.Context())
		if err != nil {
			return err
		}
		if len(args) == 1 {
			var selected []machineregistry.Machine
			for _, machine := range snapshot.Machines {
				if machine.ID == args[0] {
					selected = append(selected, machine)
				}
			}
			if len(selected) == 0 {
				return fmt.Errorf("machine %q not found: %w", args[0], machineregistry.ErrNotFound)
			}
			bindings := []machineregistry.Binding{}
			for _, binding := range snapshot.Bindings {
				if binding.MachineID == args[0] {
					bindings = append(bindings, binding)
				}
			}
			snapshot.Machines = selected
			snapshot.Bindings = bindings
			aliases := map[string]bool{}
			for _, binding := range bindings {
				if binding.Provider == "ssh" {
					aliases[strings.ToLower(binding.NativeID)] = true
				}
			}
			imports := []machineregistry.SSHImport{}
			for _, record := range snapshot.Imports {
				if aliases[strings.ToLower(record.LocalAlias)] {
					imports = append(imports, record)
				}
			}
			snapshot.Imports = imports
		}
		if showJSON {
			return writeSSHJSON(app, struct {
				Kind string `json:"kind"`
				machineregistry.Snapshot
			}{"ssh_machine_snapshot", snapshot})
		}
		for _, m := range snapshot.Machines {
			if len(args) == 0 || m.ID == args[0] {
				fmt.Fprintf(app.Out, "%s  %s  revision=%d", m.ID, m.Label, m.Revision)
				if m.MergedInto != "" {
					fmt.Fprintf(app.Out, " -> %s", m.MergedInto)
				}
				fmt.Fprintln(app.Out)
				for _, b := range snapshot.Bindings {
					if b.MachineID == m.ID {
						fmt.Fprintf(app.Out, "  %s %s (suppressed=%t)\n", b.Provider, b.NativeID, b.Suppressed)
					}
				}
			}
		}
		return nil
	}
	show.RunE = func(cmd *cobra.Command, args []string) error {
		return withSSHMachineJSON(app, showJSON, "ssh_machine_snapshot", func(a *App) error { return runShow(a, cmd, args) })
	}
	cmd.AddCommand(show)
	for _, action := range []string{"adopt", "link", "unlink", "merge"} {
		cmd.AddCommand(newSSHMachineActionCmd(app, action))
	}
	return cmd
}

func newSSHMachineActionCmd(app *App, action string) *cobra.Command {
	var machineID, into, label string
	var sources []string
	var apply, yes, jsonOut bool
	run := func(app *App, cmd *cobra.Command, _ []string) error {
		if yes && !apply {
			return asUsageError(errors.New("--yes requires --apply"))
		}
		if action == "adopt" && len(sources) == 0 && app.interactive() && !jsonOut {
			return runSSHAdoptWizard(cmd.Context(), app)
		}
		request := machineregistry.Request{Action: action, MachineID: machineID, Into: into, Label: label}
		if action != "merge" {
			inv, err := loadSSHMachineInventory(cmd.Context(), app, false, true)
			if err != nil {
				return err
			}
			for _, selector := range sources {
				found := false
				if action == "unlink" {
					snapshot, e := app.machineStore().Read(cmd.Context())
					if e != nil {
						return e
					}
					for _, b := range snapshot.Bindings {
						if (b.MachineID == machineID || b.Suppressed) && sshflow.ReferenceID(b.Provider, b.Scope, b.NativeID) == selector {
							b.MachineID = ""
							b.Suppressed = false
							request.Bindings = append(request.Bindings, b)
							found = true
						}
					}
					if !found {
						return fmt.Errorf("recorded binding %q was not found on selected machine", selector)
					}
					continue
				}
				for _, row := range inv.Machines {
					for _, ref := range row.References {
						if ref.ID == selector {
							if ref.State == "unresolved" || ref.State == "stale" {
								continue
							}
							request.Bindings = append(request.Bindings, sshflow.ReferenceBinding(ref))
							found = true
						}
					}
				}
				if !found {
					return fmt.Errorf("source %q not found; inspect dev ssh list --tailscale --lan --json", selector)
				}
			}
		}
		return runSSHMachineChange(cmd.Context(), app, request, apply, yes, jsonOut)
	}
	cmd := &cobra.Command{Use: action, Short: "Preview " + action + " of canonical machine mappings", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		kind := "ssh_machine_plan"
		if apply {
			kind = "ssh_machine_result"
		}
		return withSSHMachineJSON(app, jsonOut, kind, func(a *App) error { return run(a, cmd, args) })
	}}
	f := cmd.Flags()
	f.StringVar(&machineID, "machine", "", "exact machine UUID")
	f.StringVar(&into, "into", "", "survivor UUID for merge")
	f.StringVar(&label, "label", "", "machine display name for adoption")
	f.StringArrayVar(&sources, "source", nil, "exact source reference ID from the joined JSON list (repeatable)")
	f.BoolVar(&apply, "apply", false, "apply the reviewed registry transaction")
	f.BoolVar(&yes, "yes", false, "confirm the local mapping plan")
	f.BoolVar(&jsonOut, "json", false, "emit one registry plan or result")
	return cmd
}

func runSSHMachineChange(ctx context.Context, app *App, request machineregistry.Request, apply, yes, jsonOut bool) error {
	if request.Action == "adopt" || request.Action == "link" {
		if err := validateMachineSourceBindings(ctx, app, request.Bindings); err != nil {
			return err
		}
	}
	store := app.machineStore()
	plan, err := store.Plan(ctx, request)
	if err != nil {
		return err
	}
	if !apply {
		if jsonOut {
			return writeSSHJSON(app, struct {
				Kind string `json:"kind"`
				machineregistry.Plan
			}{"ssh_machine_plan", plan})
		}
		renderSSHMachinePlan(app, request, plan)
		return nil
	}
	if !yes {
		if jsonOut || !app.interactive() {
			return errors.New("--yes is required to apply outside an interactive terminal")
		}
		renderSSHMachinePlan(app, request, plan)
		confirmed, e := newPrompter(app).confirm("Apply this machine identity mapping?", false)
		if e != nil {
			return e
		}
		if !confirmed {
			return errPromptCanceled
		}
	}
	var result machineregistry.Result
	applyPlan := func() error {
		if request.Action == "adopt" || request.Action == "link" {
			if e := validateMachineSourceBindings(ctx, app, request.Bindings); e != nil {
				return e
			}
		}
		var e error
		result, e = store.Apply(ctx, plan)
		return e
	}
	if len(request.Bindings) > 0 && (request.Action == "adopt" || request.Action == "link") {
		err = withSSHOperationLock(ctx, app, applyPlan)
	} else {
		err = applyPlan()
	}
	if result.SchemaVersion == 0 {
		return err
	}
	if jsonOut {
		if e := writeSSHJSON(app, struct {
			Kind string `json:"kind"`
			machineregistry.Result
		}{"ssh_machine_result", result}); e != nil {
			return e
		}
	} else {
		fmt.Fprintf(app.Out, "Machine %s: %s\n", result.MachineID, result.Status)
	}
	return err
}

func renderSSHMachinePlan(app *App, request machineregistry.Request, plan machineregistry.Plan) {
	fmt.Fprintf(app.Out, "Machine %s: %s (%s)\n", plan.Action, plan.MachineID, plan.Status)
	if request.Label != "" {
		fmt.Fprintf(app.Out, "  Name: %s\n", request.Label)
	}
	if plan.Into != "" {
		fmt.Fprintf(app.Out, "  Surviving machine: %s\n", plan.Into)
	}
	for _, binding := range request.Bindings {
		fmt.Fprintf(app.Out, "  %s: %s\n", binding.Provider, binding.NativeID)
	}
	for _, effect := range plan.Effects {
		fmt.Fprintf(app.Out, "  %s\n", effect)
	}
}

func validateMachineSourceBindings(ctx context.Context, app *App, bindings []machineregistry.Binding) error {
	if len(bindings) == 0 {
		return nil
	}
	inv, err := loadSSHMachineInventory(ctx, app, false, true)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		id := sshflow.ReferenceID(binding.Provider, binding.Scope, binding.NativeID)
		found := false
		for _, row := range inv.Machines {
			for _, ref := range row.References {
				if ref.ID == id && ref.Fingerprint == binding.Fingerprint && ref.State != "unresolved" && ref.State != "stale" {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("source %s changed or is unavailable; refresh before binding", id)
		}
	}
	return nil
}

func runSSHAdoptWizard(ctx context.Context, app *App) error {
	inv, err := loadSSHMachineInventory(ctx, app, false, true)
	if err != nil {
		return err
	}
	items := []picker.Item{}
	for _, row := range inv.Machines {
		if row.MachineID == "" {
			items = append(items, picker.Item{Value: row.ID, Label: row.Label, Description: strings.Join(machineAliases(row), ", ")})
		}
	}
	if len(items) == 0 {
		return errors.New("no unregistered candidates; discover hosts or use machine link")
	}
	picked, err := sshPick(ctx, app, "Select candidate groups to enroll (each group receives its own UUID)", items, true)
	if err != nil {
		return err
	}
	requests := []machineregistry.Request{}
	for _, selection := range picked {
		for _, row := range inv.Machines {
			if row.ID != selection.Value {
				continue
			}
			label, e := newPrompter(app).line("Machine name", row.Label)
			if e != nil {
				return e
			}
			r := machineregistry.Request{Action: "adopt", Label: label}
			for _, ref := range row.References {
				r.Bindings = append(r.Bindings, sshflow.ReferenceBinding(ref))
			}
			requests = append(requests, r)
			fmt.Fprintf(app.Out, "Enroll %s: %d source bindings\n", label, len(r.Bindings))
		}
	}
	confirmed, err := newPrompter(app).confirm("Enroll these machine groups?", false)
	if err != nil {
		return err
	}
	if !confirmed {
		return errPromptCanceled
	}
	for _, r := range requests {
		if err := runSSHMachineChange(ctx, app, r, true, true, false); err != nil {
			return err
		}
	}
	return nil
}
