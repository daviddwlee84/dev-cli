package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/spf13/cobra"
)

const (
	sshCLISchemaVersion           = 1
	sshShowTimeout                = 15 * time.Second
	sshProbeTimeout               = 30 * time.Second
	sshNoninteractiveSetupTimeout = 10 * time.Minute
)

var (
	errPrimaryFleetReference   = errors.New("primary fleet configuration references SSH alias")
	errGeneratedFleetReference = errors.New("generated fleet configuration references SSH alias")

	// beforeSSHRemoveFinalCheck is a package-test seam for a registration in
	// the final generated-fleet-to-SSH deletion window.
	beforeSSHRemoveFinalCheck func()
)

func (a *App) sshHosts() (*sshhost.Service, error) {
	if a.sshHostService != nil {
		return a.sshHostService, nil
	}
	service, err := sshhost.NewDefaultService(a.sshHostRunner)
	if err != nil {
		return nil, err
	}
	a.sshHostService = service
	return service, nil
}

type sshFleetFact struct {
	Name     string `json:"name"`
	SSHAlias string `json:"ssh_alias"`
	RemoteOS string `json:"remote_os"`
	Origin   string `json:"origin"`
	Managed  bool   `json:"managed"`
}

type sshDefinitionProvenance struct {
	Source   sshhost.Location `json:"source"`
	Resolved string           `json:"resolved,omitempty"`
}

type sshDefinitionView struct {
	Alias        string                    `json:"alias"`
	Patterns     []string                  `json:"patterns"`
	Source       sshhost.Location          `json:"source"`
	Provenance   []sshDefinitionProvenance `json:"provenance,omitempty"`
	Reachability sshhost.Reachability      `json:"reachability"`
	Ownership    sshhost.Ownership         `json:"ownership"`
}

type sshAliasView struct {
	Name          string              `json:"name"`
	Status        string              `json:"status"`
	Selectable    bool                `json:"selectable"`
	Ownership     string              `json:"ownership"`
	ManagedSource string              `json:"managed_source,omitempty"`
	Definitions   []sshDefinitionView `json:"definitions"`
	Fleet         []sshFleetFact      `json:"fleet,omitempty"`
}

type sshListDocument struct {
	SchemaVersion        int                  `json:"schema_version"`
	Kind                 string               `json:"kind"`
	Complete             bool                 `json:"complete"`
	Root                 string               `json:"root"`
	RootMissing          bool                 `json:"root_missing,omitempty"`
	ManagedIncludeActive bool                 `json:"managed_include_active"`
	Aliases              []sshAliasView       `json:"aliases"`
	Diagnostics          []sshhost.Diagnostic `json:"diagnostics,omitempty"`
}

type sshInitDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	Kind          string              `json:"kind"`
	Status        string              `json:"status"`
	Plan          sshhost.InitPlan    `json:"plan"`
	Result        *sshhost.InitResult `json:"result,omitempty"`
	ErrorCode     string              `json:"error_code,omitempty"`
}

type sshShowDocument struct {
	SchemaVersion int                      `json:"schema_version"`
	Kind          string                   `json:"kind"`
	Status        string                   `json:"status"`
	Alias         string                   `json:"alias"`
	AliasStatus   string                   `json:"alias_status"`
	Definitions   []sshDefinitionView      `json:"definitions"`
	Effective     *sshhost.EffectiveConfig `json:"effective,omitempty"`
	Fleet         []sshFleetFact           `json:"fleet,omitempty"`
	Diagnostics   []sshhost.Diagnostic     `json:"diagnostics,omitempty"`
	ErrorCode     string                   `json:"error_code,omitempty"`
}

type sshProbeDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	Kind          string              `json:"kind"`
	Status        string              `json:"status"`
	Result        sshhost.ProbeResult `json:"result"`
	ErrorCode     string              `json:"error_code,omitempty"`
}

type sshFleetChange struct {
	Requested bool   `json:"requested"`
	Action    string `json:"action"`
	Name      string `json:"name,omitempty"`
	SSHAlias  string `json:"ssh_alias"`
	RemoteOS  string `json:"remote_os,omitempty"`
	Path      string `json:"path,omitempty"`
}

type sshSetupDocument struct {
	SchemaVersion   int                        `json:"schema_version"`
	Kind            string                     `json:"kind"`
	Status          string                     `json:"status"`
	Alias           string                     `json:"alias"`
	AliasClass      string                     `json:"alias_class"`
	DryRun          bool                       `json:"dry_run"`
	Definition      *sshhost.ManagedDefinition `json:"definition,omitempty"`
	ManagedPlan     *sshhost.ManagedPlan       `json:"managed_plan,omitempty"`
	KeyPlan         *sshhost.KeyPlan           `json:"key_plan,omitempty"`
	BootstrapPlan   *sshhost.BootstrapPlan     `json:"bootstrap_plan,omitempty"`
	KeyResult       *sshhost.KeyResult         `json:"key_result,omitempty"`
	ManagedResult   *sshhost.ManagedResult     `json:"managed_result,omitempty"`
	BootstrapResult *sshhost.BootstrapResult   `json:"bootstrap_result,omitempty"`
	Effective       *sshhost.EffectiveConfig   `json:"effective,omitempty"`
	Fleet           sshFleetChange             `json:"fleet"`
	FleetMembership []sshFleetFact             `json:"fleet_membership,omitempty"`
	ErrorCode       string                     `json:"error_code,omitempty"`
}

type sshRemoveDocument struct {
	SchemaVersion int                    `json:"schema_version"`
	Kind          string                 `json:"kind"`
	Status        string                 `json:"status"`
	Alias         string                 `json:"alias"`
	DryRun        bool                   `json:"dry_run"`
	Plan          *sshhost.ManagedPlan   `json:"plan,omitempty"`
	Result        *sshhost.ManagedResult `json:"result,omitempty"`
	Fleet         sshFleetChange         `json:"fleet"`
	ErrorCode     string                 `json:"error_code,omitempty"`
}

func newSSHCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh",
		Short: "Discover, configure, and bootstrap OpenSSH host aliases",
		Long: `Manage only dev-owned host fragments under ~/.ssh/dev.d while keeping
OpenSSH as the source of truth. Static listing never runs ssh or Match exec;
show, setup, and probe explicitly cross into OpenSSH evaluation or login.`,
	}
	cmd.AddCommand(
		newSSHInitCmd(app),
		newSSHListCmd(app),
		newSSHShowCmd(app),
		newSSHSetupCmd(app),
		newSSHProbeCmd(app),
		newSSHRemoveCmd(app),
	)
	return cmd
}

func newSSHInitCmd(app *App) *cobra.Command {
	var apply, yes, jsonOut bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Plan or install the dedicated dev.d Include",
		Long: `Plan the exact root SSH config change by default. --apply installs only
Include ~/.ssh/dev.d/*.conf before the first Host or Match directive.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if yes && !apply {
				return asUsageError(errors.New("--yes requires --apply"))
			}
			return runSSHInit(cmd.Context(), app, apply, yes, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "apply the planned root Include change")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the local plan without prompting")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit one versioned JSON plan or result")
	return cmd
}

func runSSHInit(ctx context.Context, app *App, apply, yes, jsonOut bool) error {
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	plan, err := service.PlanInit(ctx)
	if err != nil {
		return err
	}
	document := sshInitDocument{SchemaVersion: sshCLISchemaVersion, Kind: "ssh_init_plan", Status: "planned", Plan: plan}
	renderSSHDiagnostics(app, plan.Diagnostics)
	if plan.Action == sshhost.ActionBlocked || !plan.Ready() {
		document.Status = "blocked"
		document.ErrorCode = "blocked"
		return finishSSHInit(app, jsonOut, document, fmt.Errorf("SSH init is blocked: %w", sshhost.ErrBlocked))
	}
	if !apply {
		if plan.Action == sshhost.ActionNoop {
			document.Status = "noop"
		}
		return finishSSHInit(app, jsonOut, document, nil)
	}

	if !yes {
		if jsonOut || !app.interactive() {
			document.Status = "confirmation_required"
			document.ErrorCode = "confirmation_required"
			return finishSSHInit(app, jsonOut, document, errors.New("--yes is required to apply SSH init without an interactive terminal"))
		}
		renderSSHInitPlan(app, plan)
		confirmed, promptErr := newPrompter(app).confirm("Apply this SSH root config plan?", false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			return errPromptCanceled
		}
	}
	result, err := service.ApplyInit(ctx, plan)
	document.Kind = "ssh_init_result"
	document.Result = &result
	if err != nil {
		document.Status = "failed"
		document.ErrorCode = sshErrorCode(err)
		return finishSSHInit(app, jsonOut, document, err)
	}
	if result.Changed {
		document.Status = "changed"
	} else {
		document.Status = "noop"
	}
	return finishSSHInit(app, jsonOut, document, nil)
}

func finishSSHInit(app *App, jsonOut bool, document sshInitDocument, err error) error {
	if jsonOut {
		if encodeErr := writeSSHJSON(app, document); encodeErr != nil {
			return encodeErr
		}
	} else if document.Result != nil {
		fmt.Fprintf(app.Out, "SSH init %s: %s\n", document.Status, config.Contract(document.Result.Path))
	} else {
		renderSSHInitPlan(app, document.Plan)
	}
	return err
}

func renderSSHInitPlan(app *App, plan sshhost.InitPlan) {
	fmt.Fprintf(app.Out, "SSH init plan: %s\n", plan.Action)
	fmt.Fprintf(app.Out, "  root:    %s\n", config.Contract(plan.Path))
	fmt.Fprintf(app.Out, "  include: Include %s\n", plan.Include)
	fmt.Fprintf(app.Out, "  managed: %s\n", config.Contract(plan.ManagedDir))
}

func newSSHListCmd(app *App) *cobra.Command {
	var jsonOut bool
	var format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Statically list exact SSH aliases and their definitions",
		Long: `Walk the active user Include closure without invoking ssh, a resolver,
Match exec, an agent, or the network. --format tsv emits one definition per row:
alias, status, ownership, source, line, comma-separated fleet names.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if jsonOut && format != "" {
				return asUsageError(errors.New("--json and --format are mutually exclusive"))
			}
			if format != "" && format != "tsv" {
				return asUsageError(fmt.Errorf("unsupported --format %q (want tsv)", format))
			}
			return runSSHList(cmd.Context(), app, jsonOut, format)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit one versioned JSON object")
	cmd.Flags().StringVar(&format, "format", "", "machine format: tsv")
	registerFlagCompletion(cmd, "format", fixedCompletions("tsv"))
	return cmd
}

func runSSHList(ctx context.Context, app *App, jsonOut bool, format string) error {
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	inventory, err := service.Discover(ctx)
	if err != nil {
		return err
	}
	document := sshListDocument{
		SchemaVersion:        sshCLISchemaVersion,
		Kind:                 "ssh_list",
		Complete:             inventory.Complete,
		Root:                 inventory.Root,
		RootMissing:          inventory.RootMissing,
		ManagedIncludeActive: inventory.ManagedIncludeActive,
		Aliases:              make([]sshAliasView, 0, len(inventory.Aliases)),
		Diagnostics:          append([]sshhost.Diagnostic(nil), inventory.Diagnostics...),
	}
	fleetConfig, fleetErr := loadFleetConfig(app)
	if fleetErr != nil {
		document.Complete = false
		document.Diagnostics = append(document.Diagnostics, sshhost.Diagnostic{
			Code: "fleet_config_invalid", Message: "dev fleet membership could not be loaded", Path: fleetConfigPath(app), Incomplete: true,
		})
		app.warnf("SSH list could not load fleet membership: %v", fleetErr)
	}
	fleetFacts := indexFleetFacts(fleetConfig)
	for _, alias := range inventory.Aliases {
		view := aliasView(alias, fleetFacts[foldFleetAlias(alias.Name)])
		document.Aliases = append(document.Aliases, view)
	}
	renderSSHDiagnostics(app, inventory.Diagnostics)
	if jsonOut {
		return writeSSHJSON(app, document)
	}
	if format == "tsv" {
		renderSSHListTSV(app, document.Aliases)
		return nil
	}
	renderSSHListHuman(app, document)
	return nil
}

func aliasView(alias sshhost.Alias, facts []sshFleetFact) sshAliasView {
	status := aliasStatus(alias)
	view := sshAliasView{
		Name:        alias.Name,
		Status:      status,
		Selectable:  status == "active",
		Ownership:   aliasOwnership(alias),
		Definitions: safeSSHDefinitions(alias.Definitions),
		Fleet:       facts,
	}
	for _, definition := range alias.Definitions {
		if definition.Ownership == sshhost.OwnershipManaged {
			view.ManagedSource = definition.Source.Path
			break
		}
	}
	return view
}

func safeSSHDefinitions(definitions []sshhost.AliasDefinition) []sshDefinitionView {
	views := make([]sshDefinitionView, 0, len(definitions))
	for _, definition := range definitions {
		view := sshDefinitionView{
			Alias: definition.Alias, Patterns: append([]string(nil), definition.Patterns...),
			Source: definition.Source, Reachability: definition.Reachability, Ownership: definition.Ownership,
		}
		for _, frame := range definition.Provenance {
			view.Provenance = append(view.Provenance, sshDefinitionProvenance{Source: frame.Source, Resolved: frame.Resolved})
		}
		views = append(views, view)
	}
	return views
}

func aliasStatus(alias sshhost.Alias) string {
	if alias.Conflict {
		return "conflict"
	}
	for _, definition := range alias.Definitions {
		if definition.Reachability == sshhost.Unknown {
			return "unknown"
		}
		if definition.Reachability == sshhost.Unreachable {
			return "inactive"
		}
	}
	if len(alias.Definitions) == 0 {
		return "inactive"
	}
	return "active"
}

func aliasOwnership(alias sshhost.Alias) string {
	seen := map[sshhost.Ownership]bool{}
	for _, definition := range alias.Definitions {
		seen[definition.Ownership] = true
	}
	if seen[sshhost.OwnershipConflict] {
		return string(sshhost.OwnershipConflict)
	}
	if seen[sshhost.OwnershipManaged] && seen[sshhost.OwnershipForeign] {
		return "mixed"
	}
	if seen[sshhost.OwnershipManaged] {
		return string(sshhost.OwnershipManaged)
	}
	return string(sshhost.OwnershipForeign)
}

func renderSSHListHuman(app *App, document sshListDocument) {
	table := app.newTable("ALIAS", "STATUS", "OWNER", "SOURCE", "FLEET")
	for _, alias := range document.Aliases {
		source := "—"
		if len(alias.Definitions) > 0 {
			source = fmt.Sprintf("%s:%d", config.Contract(alias.Definitions[0].Source.Path), alias.Definitions[0].Source.Line)
			if len(alias.Definitions) > 1 {
				source += fmt.Sprintf(" (+%d)", len(alias.Definitions)-1)
			}
		}
		table.Add(alias.Name, alias.Status, alias.Ownership, source, strings.Join(fleetFactNames(alias.Fleet), ","))
	}
	table.Render(app.Out)
	if len(document.Aliases) == 0 {
		fmt.Fprintf(app.Out, "No exact SSH aliases found under %s.\n", config.Contract(document.Root))
	}
	if !document.Complete {
		fmt.Fprintln(app.Err, "dev: warning: SSH alias discovery is incomplete; unknown declarations are not reported as usable")
	}
}

func renderSSHListTSV(app *App, aliases []sshAliasView) {
	for _, alias := range aliases {
		fleetNames := strings.Join(fleetFactNames(alias.Fleet), ",")
		for _, definition := range alias.Definitions {
			fmt.Fprintf(app.Out, "%s\t%s\t%s\t%s\t%d\t%s\n",
				completionDescriptionSanitizer.Replace(alias.Name), completionDescriptionSanitizer.Replace(alias.Status), completionDescriptionSanitizer.Replace(string(definition.Ownership)),
				completionDescriptionSanitizer.Replace(definition.Source.Path), definition.Source.Line, completionDescriptionSanitizer.Replace(fleetNames))
		}
	}
}

func fleetFactNames(facts []sshFleetFact) []string {
	names := make([]string, 0, len(facts))
	for _, fact := range facts {
		names = append(names, fact.Name)
	}
	return names
}

func newSSHShowCmd(app *App) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show <alias>",
		Short: "Show static definitions and plain ssh -G effective values",
		Long: `Static definitions retain source provenance. Effective values come from
plain ssh -G <alias>, so configured Match exec and resolver behavior may run;
dev does not add -F or parse ssh -vv output.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), sshShowTimeout)
			defer cancel()
			return runSSHShow(ctx, app, args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit one versioned JSON object")
	cmd.ValidArgsFunction = completeSSHAliases(app)
	return cmd
}

func runSSHShow(ctx context.Context, app *App, alias string, jsonOut bool) error {
	if err := sshhost.ValidateLookupAlias(alias); err != nil {
		return asUsageError(err)
	}
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	inventory, err := service.Discover(ctx)
	if err != nil {
		return err
	}
	found, ok := inventory.Find(alias)
	if !ok {
		document := sshShowDocument{
			SchemaVersion: sshCLISchemaVersion, Kind: "ssh_show", Status: "not_found",
			Alias: alias, AliasStatus: "inactive", Definitions: []sshDefinitionView{},
			Diagnostics: append([]sshhost.Diagnostic(nil), inventory.Diagnostics...), ErrorCode: "not_found",
		}
		return finishSSHShow(app, jsonOut, document, fmt.Errorf("unknown exact SSH alias %q", alias))
	}
	document := sshShowDocument{
		SchemaVersion: sshCLISchemaVersion,
		Kind:          "ssh_show",
		Status:        "ready",
		Alias:         found.Name,
		AliasStatus:   aliasStatus(found),
		Definitions:   safeSSHDefinitions(found.Definitions),
		Diagnostics:   append([]sshhost.Diagnostic(nil), inventory.Diagnostics...),
	}
	if cfg, fleetErr := loadFleetConfig(app); fleetErr == nil {
		document.Fleet = indexFleetFacts(cfg)[foldFleetAlias(found.Name)]
	} else {
		document.Diagnostics = append(document.Diagnostics, sshhost.Diagnostic{Code: "fleet_config_invalid", Message: "dev fleet membership could not be loaded", Incomplete: true})
		app.warnf("SSH show could not load fleet membership: %v", fleetErr)
	}
	app.warnf("ssh show runs plain ssh -G; configured Match exec and resolver behavior may run")
	effective, effectiveErr := service.Effective(ctx, alias)
	if effectiveErr != nil {
		document.Status = "failed"
		document.ErrorCode = sshErrorCode(effectiveErr)
		return finishSSHShow(app, jsonOut, document, effectiveErr)
	}
	document.Effective = &effective
	return finishSSHShow(app, jsonOut, document, nil)
}

func finishSSHShow(app *App, jsonOut bool, document sshShowDocument, err error) error {
	if jsonOut {
		if encodeErr := writeSSHJSON(app, document); encodeErr != nil {
			return encodeErr
		}
	} else {
		fmt.Fprintf(app.Out, "SSH alias %s (%s)\n", document.Alias, document.AliasStatus)
		definitions := app.newTable("OWNER", "REACHABILITY", "SOURCE", "PATTERNS")
		for _, definition := range document.Definitions {
			definitions.Add(string(definition.Ownership), string(definition.Reachability),
				fmt.Sprintf("%s:%d", config.Contract(definition.Source.Path), definition.Source.Line), strings.Join(definition.Patterns, " "))
		}
		definitions.Render(app.Out)
		if document.Effective != nil {
			effective := document.Effective
			fmt.Fprintln(app.Out, "\nEffective (plain ssh -G):")
			fmt.Fprintf(app.Out, "  HostName:       %s\n", dash(effective.HostName))
			fmt.Fprintf(app.Out, "  User:           %s\n", dash(effective.User))
			fmt.Fprintf(app.Out, "  Port:           %d\n", effective.Port)
			fmt.Fprintf(app.Out, "  ProxyJump:      %s\n", dash(effective.ProxyJump))
			fmt.Fprintf(app.Out, "  IdentityFile:   %s\n", dash(strings.Join(effective.IdentityFiles, ", ")))
			identitiesOnly := "—"
			if effective.IdentitiesOnly != nil {
				identitiesOnly = strconv.FormatBool(*effective.IdentitiesOnly)
			}
			fmt.Fprintf(app.Out, "  IdentitiesOnly: %s\n", identitiesOnly)
		}
		if len(document.Fleet) > 0 {
			fmt.Fprintf(app.Out, "\nFleet: %s\n", strings.Join(fleetFactNames(document.Fleet), ", "))
		}
	}
	return err
}

type sshSetupOptions struct {
	hostName                   string
	user                       string
	port                       int
	proxyJump                  string
	identityFile               string
	identitiesOnly             bool
	configOnly                 bool
	key                        string
	generateKey                bool
	keyPath                    string
	comment                    string
	noPassphrase               bool
	targetOS                   string
	hopOS                      []string
	installOnWorkingJump       bool
	windowsAdminAuthorizedKeys bool
	fleet                      bool
	fleetName                  string
	dryRun                     bool
	yes                        bool
	json                       bool
	connectionChanged          bool
	hostNameChanged            bool
	userChanged                bool
	portChanged                bool
	proxyJumpChanged           bool
	identityFileChanged        bool
	identitiesOnlyChanged      bool
}

func newSSHSetupCmd(app *App) *cobra.Command {
	var options sshSetupOptions
	cmd := &cobra.Command{
		Use:   "setup <alias>",
		Short: "Create or reconcile an alias, install a public key, and optionally register fleet",
		Long: `Unknown aliases may become strict dev-owned fragments; existing managed aliases
may be reconciled. Foreign definitions are never edited. Full setup requires an
explicit --key or --generate-key in this conservative first-stage wizard.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.hostNameChanged = cmd.Flags().Changed("hostname")
			options.userChanged = cmd.Flags().Changed("user")
			options.portChanged = cmd.Flags().Changed("port")
			options.proxyJumpChanged = cmd.Flags().Changed("proxy-jump")
			options.identityFileChanged = cmd.Flags().Changed("identity-file")
			options.identitiesOnlyChanged = cmd.Flags().Changed("identities-only")
			options.connectionChanged = options.hostNameChanged || options.userChanged || options.portChanged ||
				options.proxyJumpChanged || options.identityFileChanged || options.identitiesOnlyChanged
			if err := validateSSHSetupFlags(cmd, options); err != nil {
				return asUsageError(err)
			}
			ctx := cmd.Context()
			if options.json || !app.interactive() {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, sshNoninteractiveSetupTimeout)
				defer cancel()
			}
			return runSSHSetup(ctx, app, args[0], options)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.hostName, "hostname", "", "managed HostName value")
	flags.StringVar(&options.user, "user", "", "managed User value")
	flags.IntVar(&options.port, "port", 0, "managed SSH port")
	flags.StringVar(&options.proxyJump, "proxy-jump", "", "managed ProxyJump value")
	flags.StringVar(&options.identityFile, "identity-file", "", "managed IdentityFile value")
	flags.BoolVar(&options.identitiesOnly, "identities-only", false, "set managed IdentitiesOnly=yes (explicit false writes no)")
	flags.BoolVar(&options.configOnly, "config-only", false, "stop after local config verification")
	flags.StringVar(&options.key, "key", "", "existing public key or identity path to install")
	flags.BoolVar(&options.generateKey, "generate-key", false, "generate a new Ed25519 key pair")
	flags.StringVar(&options.keyPath, "key-path", "", "destination identity path for --generate-key")
	flags.StringVar(&options.comment, "comment", "", "public key comment for --generate-key")
	flags.BoolVar(&options.noPassphrase, "no-passphrase", false, "generate without a passphrase (required outside a TTY)")
	flags.StringVar(&options.targetOS, "target-os", "", "target operating system: posix or windows")
	flags.StringArrayVar(&options.hopOS, "hop-os", nil, "route OS override alias=posix|windows (repeatable)")
	flags.BoolVar(&options.installOnWorkingJump, "install-on-working-jump", false, "install the selected key on already-working jump hosts")
	flags.BoolVar(&options.windowsAdminAuthorizedKeys, "windows-admin-authorized-keys", false, "allow the Windows administrators_authorized_keys path")
	flags.BoolVar(&options.fleet, "fleet", false, "register the verified alias in dev fleet")
	flags.StringVar(&options.fleetName, "fleet-name", "", "fleet profile name (default: alias)")
	flags.BoolVar(&options.dryRun, "dry-run", false, "render static local plans without running OpenSSH or writing")
	flags.BoolVar(&options.yes, "yes", false, "confirm the local plan without prompting")
	flags.BoolVar(&options.json, "json", false, "emit exactly one versioned JSON plan or result")
	registerFlagCompletion(cmd, "target-os", fixedCompletions(fleet.RemoteOSPOSIX, fleet.RemoteOSWindows))
	registerFlagCompletion(cmd, "proxy-jump", completeSSHFlagAliases(app))
	registerFlagCompletion(cmd, "hop-os", completeSSHOSOverrides(app))
	cmd.ValidArgsFunction = completeSSHAliases(app)
	return cmd
}

func validateSSHSetupFlags(cmd *cobra.Command, options sshSetupOptions) error {
	if options.key != "" && options.generateKey {
		return errors.New("--key and --generate-key are mutually exclusive")
	}
	if !options.generateKey && (cmd.Flags().Changed("key-path") || cmd.Flags().Changed("comment") || options.noPassphrase) {
		return errors.New("--key-path, --comment, and --no-passphrase require --generate-key")
	}
	if options.portChanged && (options.port < 1 || options.port > 65535) {
		return errors.New("--port must be between 1 and 65535")
	}
	if options.fleetName != "" && !options.fleet {
		return errors.New("--fleet-name requires --fleet")
	}
	if options.configOnly && (options.key != "" || options.generateKey || options.fleet || options.targetOS != "" || len(options.hopOS) > 0 || options.installOnWorkingJump || options.windowsAdminAuthorizedKeys) {
		return errors.New("--config-only cannot be combined with key, route, bootstrap, or fleet flags")
	}
	if options.targetOS != "" {
		if _, err := parseSSHRemoteOS(options.targetOS); err != nil {
			return err
		}
	}
	if _, err := parseSSHOSOverrides(options.hopOS); err != nil {
		return err
	}
	if !options.configOnly && !options.dryRun && options.key == "" && !options.generateKey {
		return errors.New("full setup requires explicit --key or --generate-key")
	}
	return nil
}

func runSSHSetup(ctx context.Context, app *App, alias string, options sshSetupOptions) error {
	if err := sshhost.ValidateLookupAlias(alias); err != nil {
		return asUsageError(err)
	}
	interactiveMode := !options.json && app.interactive()
	if !options.configOnly && !options.dryRun && !interactiveMode && options.targetOS == "" {
		return asUsageError(errors.New("non-interactive full setup requires --target-os"))
	}
	if options.dryRun {
		return runSSHSetupOperation(ctx, app, alias, options)
	}
	invoked := false
	err := withSSHOperationLock(ctx, app, func() error {
		invoked = true
		return runSSHSetupOperation(ctx, app, alias, options)
	})
	if invoked {
		return err
	}
	document := sshSetupDocument{
		SchemaVersion: sshCLISchemaVersion,
		Kind:          "ssh_setup_result",
		Status:        "failed",
		Alias:         alias,
		DryRun:        options.dryRun,
		Fleet:         sshFleetChange{Requested: options.fleet, Action: "not_requested", SSHAlias: alias},
		ErrorCode:     sshErrorCode(err),
	}
	return finishSSHSetup(app, options.json, document, err)
}

func runSSHSetupOperation(ctx context.Context, app *App, alias string, options sshSetupOptions) error {
	interactiveMode := !options.json && app.interactive()
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	document := sshSetupDocument{
		SchemaVersion: sshCLISchemaVersion,
		Kind:          "ssh_setup_result",
		Status:        "planning",
		Alias:         alias,
		DryRun:        options.dryRun,
		Fleet:         sshFleetChange{Requested: options.fleet, Action: "not_requested", SSHAlias: alias},
	}
	if options.dryRun {
		document.Kind = "ssh_setup_plan"
	}
	inventory, err := service.Discover(ctx)
	if err != nil {
		return finishSSHSetup(app, options.json, document, err)
	}
	aliasClass, definition, classifyErr := classifySetupAlias(service, alias, inventory)
	document.AliasClass = aliasClass
	if classifyErr != nil {
		document.Status = "blocked"
		document.ErrorCode = sshErrorCode(classifyErr)
		return finishSSHSetup(app, options.json, document, classifyErr)
	}
	if aliasClass == "foreign" && options.connectionChanged {
		err := errors.New("connection-field flags are not allowed for a foreign SSH alias")
		document.Status = "blocked"
		document.ErrorCode = "foreign_alias"
		return finishSSHSetup(app, options.json, document, err)
	}

	ownsDefinition := aliasClass == "new" || aliasClass == "managed"
	if ownsDefinition {
		if aliasClass == "new" {
			definition = sshhost.ManagedDefinition{Alias: alias}
		}
		if err := mergeManagedDefinition(app, &definition, options, interactiveMode); err != nil {
			document.Status = "blocked"
			document.ErrorCode = sshErrorCode(err)
			return finishSSHSetup(app, options.json, document, err)
		}
	}

	var keyPlan *sshhost.KeyPlan
	if !options.configOnly && (options.key != "" || options.generateKey) {
		planned, planErr := planSSHSetupKey(ctx, app, service, options, interactiveMode)
		keyPlan = &planned
		document.KeyPlan = keyPlan
		if planErr != nil {
			document.Status = "blocked"
			document.ErrorCode = sshErrorCode(planErr)
			return finishSSHSetup(app, options.json, document, planErr)
		}
		if ownsDefinition && !options.identityFileChanged && planned.IdentityFile != "" {
			definition.IdentityFile = planned.IdentityFile
		}
	}

	if ownsDefinition {
		document.Definition = cloneSSHManagedDefinition(definition)
		managedPlan, planErr := service.PlanUpsert(ctx, definition)
		document.ManagedPlan = &managedPlan
		if planErr != nil {
			document.Status = "blocked"
			document.ErrorCode = sshErrorCode(planErr)
			return finishSSHSetup(app, options.json, document, planErr)
		}
		renderSSHDiagnostics(app, managedPlan.Diagnostics)
		if !managedPlan.Ready() || managedPlan.Action == sshhost.ActionBlocked {
			err := fmt.Errorf("managed SSH plan is blocked: %w", sshhost.ErrBlocked)
			document.Status = "blocked"
			document.ErrorCode = "managed_plan_blocked"
			return finishSSHSetup(app, options.json, document, err)
		}
	}
	if keyPlan != nil {
		renderSSHDiagnostics(app, keyPlan.Diagnostics)
		if !keyPlan.Ready() || keyPlan.Action == sshhost.ActionBlocked {
			err := fmt.Errorf("SSH key plan is blocked: %w", sshhost.ErrBlocked)
			document.Status = "blocked"
			document.ErrorCode = "key_plan_blocked"
			return finishSSHSetup(app, options.json, document, err)
		}
	}

	targetOS, _ := parseSSHRemoteOS(options.targetOS)
	overrides, _ := parseSSHOSOverrides(options.hopOS)
	if options.fleet {
		if targetOS == sshhost.RemoteOSUnknown {
			err := errors.New("--fleet requires --target-os posix or windows")
			document.Status = "blocked"
			document.ErrorCode = "target_os_required"
			return finishSSHSetup(app, options.json, document, err)
		}
		change, facts, fleetErr := planSSHFleetRegistration(app, alias, options.fleetName, targetOS)
		document.Fleet = change
		document.FleetMembership = facts
		if fleetErr != nil {
			document.Status = "blocked"
			document.ErrorCode = sshErrorCode(fleetErr)
			return finishSSHSetup(app, options.json, document, fleetErr)
		}
	} else if cfg, fleetErr := loadFleetConfig(app); fleetErr == nil {
		document.FleetMembership = indexFleetFacts(cfg)[foldFleetAlias(alias)]
	} else {
		app.warnf("SSH setup could not load fleet membership: %v", fleetErr)
	}

	if options.dryRun {
		bootstrapPlan, planErr := service.PlanBootstrap(sshhost.BootstrapRequest{
			Alias: alias, TargetRemoteOS: targetOS, OSOverrides: overrides,
			InstallOnWorkingJump:            options.installOnWorkingJump,
			AllowWindowsAdminAuthorizedKeys: options.windowsAdminAuthorizedKeys,
		})
		document.BootstrapPlan = &bootstrapPlan
		if planErr != nil {
			document.Status = "blocked"
			document.ErrorCode = sshErrorCode(planErr)
			return finishSSHSetup(app, options.json, document, planErr)
		}
		document.Status = "planned"
		return finishSSHSetup(app, options.json, document, nil)
	}

	localApply := keyPlan != nil || document.ManagedPlan != nil
	if localApply && !options.yes {
		if !interactiveMode {
			document.Status = "confirmation_required"
			document.ErrorCode = "confirmation_required"
			return finishSSHSetup(app, options.json, document, errors.New("--yes is required to apply SSH setup without an interactive terminal"))
		}
		renderSSHSetupPlan(app, document)
		confirmed, promptErr := newPrompter(app).confirm("Apply this local SSH setup plan?", false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			return errPromptCanceled
		}
	}

	var keyResult sshhost.KeyResult
	if keyPlan != nil {
		app.warnf("applying SSH key plan for %s", alias)
		applied, applyErr := service.ApplyKey(ctx, *keyPlan)
		document.KeyResult = &applied
		if applyErr != nil {
			document.Status = "failed"
			document.ErrorCode = sshErrorCode(applyErr)
			return finishSSHSetup(app, options.json, document, applyErr)
		}
		keyResult = applied
	}
	if document.ManagedPlan != nil {
		app.warnf("applying managed SSH config for %s", alias)
		applied, applyErr := service.ApplyManaged(ctx, *document.ManagedPlan)
		document.ManagedResult = &applied
		if applyErr != nil {
			document.Status = setupFailureStatus(document)
			document.ErrorCode = sshErrorCode(applyErr)
			return finishSSHSetup(app, options.json, document, applyErr)
		}
	}
	if options.configOnly {
		if aliasClass == "foreign" {
			app.warnf("config-only verification runs plain ssh -G; configured Match exec and resolver behavior may run")
			effective, effectiveErr := service.Effective(ctx, alias)
			if effectiveErr != nil {
				document.Status = "failed"
				document.ErrorCode = sshErrorCode(effectiveErr)
				return finishSSHSetup(app, options.json, document, effectiveErr)
			}
			document.Effective = &effective
		}
		document.Status = "ready"
		return finishSSHSetup(app, options.json, document, nil)
	}

	app.warnf("resolving SSH route for %s with plain ssh -G", alias)
	route, routeErr := service.ResolveRoute(ctx, sshhost.RouteRequest{Alias: alias, TargetRemoteOS: targetOS, OSOverrides: overrides})
	if routeErr != nil {
		document.Status = setupFailureStatus(document)
		document.ErrorCode = sshErrorCode(routeErr)
		return finishSSHSetup(app, options.json, document, routeErr)
	}
	route, overrides, routeErr = completeSSHRouteOS(ctx, app, service, route, alias, targetOS, overrides, interactiveMode)
	if routeErr != nil {
		document.Status = setupFailureStatus(document)
		document.ErrorCode = sshErrorCode(routeErr)
		return finishSSHSetup(app, options.json, document, routeErr)
	}

	app.warnf("bootstrapping public-key authentication for %s", alias)
	bootstrap, bootstrapErr := service.Bootstrap(ctx, sshhost.BootstrapRequest{
		Alias:                           alias,
		Route:                           route,
		Key:                             keyResult,
		TargetRemoteOS:                  targetOS,
		OSOverrides:                     overrides,
		Interactive:                     interactiveMode,
		InstallOnWorkingJump:            options.installOnWorkingJump,
		AllowWindowsAdminAuthorizedKeys: options.windowsAdminAuthorizedKeys,
	})
	document.BootstrapResult = &bootstrap
	if bootstrapErr != nil {
		document.Status = setupFailureStatus(document)
		document.ErrorCode = sshErrorCode(bootstrapErr)
		return finishSSHSetup(app, options.json, document, bootstrapErr)
	}
	if !bootstrap.Ready || !bootstrap.FleetReady {
		document.Status = "partial"
		document.ErrorCode = "bootstrap_partial"
		return finishSSHSetup(app, options.json, document, errors.New("SSH bootstrap is partial; rerun setup after resolving the reported hop"))
	}
	if options.fleet {
		app.warnf("registering verified SSH alias %s in dev fleet", alias)
		_, fleetErr := fleet.WriteManagedFragment(ctx, fleetConfigPath(app), fleet.ManagedHost{
			Name: document.Fleet.Name, SSHAlias: alias, RemoteOS: string(bootstrap.TargetRemoteOS),
		}, nil)
		if fleetErr != nil {
			document.Status = "partial"
			document.Fleet.Action = "failed"
			document.ErrorCode = sshErrorCode(fleetErr)
			return finishSSHSetup(app, options.json, document, fleetErr)
		}
		document.Fleet.Action = "registered"
	}
	document.Status = "ready"
	return finishSSHSetup(app, options.json, document, nil)
}

func classifySetupAlias(service *sshhost.Service, alias string, inventory sshhost.Inventory) (string, sshhost.ManagedDefinition, error) {
	found, known := inventory.Find(alias)
	if !known {
		for _, declaration := range inventory.Declarations {
			for _, exact := range declaration.ExactAliases {
				if strings.EqualFold(exact, alias) {
					return "inactive", sshhost.ManagedDefinition{}, fmt.Errorf("exact SSH alias %q is statically inactive: %w", alias, sshhost.ErrBlocked)
				}
			}
		}
		if !inventory.Complete {
			return "unknown", sshhost.ManagedDefinition{}, fmt.Errorf("SSH config discovery is incomplete, so alias %q cannot be proven absent: %w", alias, sshhost.ErrBlocked)
		}
		if err := sshhost.ValidateManagedAlias(alias); err != nil {
			return "new", sshhost.ManagedDefinition{}, fmt.Errorf("unknown aliases must use the managed alias grammar: %w", err)
		}
		return "new", sshhost.ManagedDefinition{Alias: alias}, nil
	}
	status := aliasStatus(found)
	if status != "active" {
		return status, sshhost.ManagedDefinition{}, fmt.Errorf("exact SSH alias %q has static status %s: %w", alias, status, sshhost.ErrBlocked)
	}
	if !inventory.Complete {
		return "unknown", sshhost.ManagedDefinition{}, fmt.Errorf("SSH config discovery is incomplete, so alias %q cannot be proven non-conflicting: %w", alias, sshhost.ErrBlocked)
	}
	if len(found.Definitions) != 1 {
		return "conflict", sshhost.ManagedDefinition{}, fmt.Errorf("exact SSH alias %q has conflicting definitions: %w", alias, sshhost.ErrBlocked)
	}
	switch found.Definitions[0].Ownership {
	case sshhost.OwnershipManaged:
		managed, err := service.InspectManaged(alias)
		if err != nil {
			return "managed", sshhost.ManagedDefinition{}, err
		}
		return "managed", managed.Definition, nil
	case sshhost.OwnershipForeign:
		return "foreign", sshhost.ManagedDefinition{}, nil
	default:
		return "conflict", sshhost.ManagedDefinition{}, fmt.Errorf("exact SSH alias %q has conflicting ownership: %w", alias, sshhost.ErrBlocked)
	}
}

func mergeManagedDefinition(app *App, definition *sshhost.ManagedDefinition, options sshSetupOptions, interactiveMode bool) error {
	if options.hostNameChanged {
		definition.HostName = options.hostName
	}
	if definition.HostName == "" && interactiveMode {
		value, err := newPrompter(app).line("HostName", "")
		if err != nil {
			return err
		}
		definition.HostName = value
	}
	if options.userChanged {
		definition.User = options.user
	}
	if options.portChanged {
		definition.Port = options.port
	}
	if options.proxyJumpChanged {
		definition.ProxyJump = options.proxyJump
	}
	if options.identityFileChanged {
		definition.IdentityFile = options.identityFile
	}
	if options.identitiesOnlyChanged {
		value := options.identitiesOnly
		definition.IdentitiesOnly = &value
	}
	if definition.HostName == "" {
		return errors.New("a new managed alias requires --hostname")
	}
	return sshhost.ValidateManagedDefinition(*definition)
}

func planSSHSetupKey(ctx context.Context, app *App, service *sshhost.Service, options sshSetupOptions, interactiveMode bool) (sshhost.KeyPlan, error) {
	request := sshhost.KeyRequest{
		Interactive:  interactiveMode && !options.noPassphrase,
		AllowDerive:  options.yes,
		NoPassphrase: options.noPassphrase,
	}
	if options.generateKey {
		request.Operation = sshhost.KeyGenerate
		request.DestinationIdentity = options.keyPath
		request.Comment = options.comment
	} else {
		request.Operation = sshhost.KeyUse
		request.Path = options.key
	}
	plan, err := service.PlanKey(ctx, request)
	if err != nil {
		return plan, err
	}
	if plan.Action == sshhost.ActionBlocked && hasSSHDiagnostic(plan.Diagnostics, "derive_confirmation_required") && interactiveMode && !options.dryRun {
		confirmed, promptErr := newPrompter(app).confirm("Derive and save the missing public key companion?", false)
		if promptErr != nil {
			return plan, promptErr
		}
		if !confirmed {
			return plan, errPromptCanceled
		}
		request.AllowDerive = true
		return service.PlanKey(ctx, request)
	}
	return plan, nil
}

func cloneSSHManagedDefinition(definition sshhost.ManagedDefinition) *sshhost.ManagedDefinition {
	copy := definition
	if definition.IdentitiesOnly != nil {
		value := *definition.IdentitiesOnly
		copy.IdentitiesOnly = &value
	}
	return &copy
}

func parseSSHRemoteOS(value string) (sshhost.RemoteOS, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return sshhost.RemoteOSUnknown, nil
	case fleet.RemoteOSPOSIX:
		return sshhost.RemoteOSPOSIX, nil
	case fleet.RemoteOSWindows:
		return sshhost.RemoteOSWindows, nil
	default:
		return sshhost.RemoteOSUnknown, fmt.Errorf("remote OS %q must be posix or windows", value)
	}
}

func parseSSHOSOverrides(values []string) ([]sshhost.RemoteOSOverride, error) {
	overrides := make([]sshhost.RemoteOSOverride, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		alias, rawOS, ok := strings.Cut(value, "=")
		if !ok || alias == "" || rawOS == "" {
			return nil, fmt.Errorf("--hop-os %q must be alias=posix|windows", value)
		}
		if err := sshhost.ValidateLookupAlias(alias); err != nil {
			return nil, fmt.Errorf("--hop-os %q: %w", value, err)
		}
		key := strings.ToLower(alias)
		if seen[key] {
			return nil, fmt.Errorf("duplicate --hop-os override for %q", alias)
		}
		seen[key] = true
		remoteOS, err := parseSSHRemoteOS(rawOS)
		if err != nil || remoteOS == sshhost.RemoteOSUnknown {
			return nil, fmt.Errorf("--hop-os %q must be alias=posix|windows", value)
		}
		overrides = append(overrides, sshhost.RemoteOSOverride{Alias: alias, RemoteOS: remoteOS})
	}
	return overrides, nil
}

func completeSSHRouteOS(ctx context.Context, app *App, service *sshhost.Service, route sshhost.Route, alias string, targetOS sshhost.RemoteOS, overrides []sshhost.RemoteOSOverride, interactiveMode bool) (sshhost.Route, []sshhost.RemoteOSOverride, error) {
	missing := make([]sshhost.RouteHop, 0)
	for _, hop := range route.Hops {
		if hop.RemoteOS == sshhost.RemoteOSUnknown {
			missing = append(missing, hop)
		}
	}
	if len(missing) == 0 {
		return route, overrides, nil
	}
	if !interactiveMode {
		names := make([]string, 0, len(missing))
		for _, hop := range missing {
			names = append(names, hop.Alias)
		}
		return route, overrides, fmt.Errorf("remote OS is unknown for %s; add --hop-os alias=posix|windows", strings.Join(names, ", "))
	}
	prompter := newPrompter(app)
	for _, hop := range missing {
		choice, err := prompter.choice("Remote OS for "+hop.Alias, "posix", "posix, windows", map[string]string{
			"posix": "posix", "p": "posix", "windows": "windows", "w": "windows",
		})
		if err != nil {
			return route, overrides, err
		}
		remoteOS, _ := parseSSHRemoteOS(choice)
		overrides = append(overrides, sshhost.RemoteOSOverride{Alias: hop.Alias, RemoteOS: remoteOS})
	}
	resolved, err := service.ResolveRoute(ctx, sshhost.RouteRequest{Alias: alias, TargetRemoteOS: targetOS, OSOverrides: overrides})
	return resolved, overrides, err
}

func planSSHFleetRegistration(app *App, alias, name string, remoteOS sshhost.RemoteOS) (sshFleetChange, []sshFleetFact, error) {
	if name == "" {
		name = alias
	}
	change := sshFleetChange{Requested: true, Action: "planned", Name: name, SSHAlias: alias, RemoteOS: string(remoteOS)}
	path, err := fleet.ManagedFragmentPath(fleetConfigPath(app), alias)
	if err != nil {
		return change, nil, err
	}
	change.Path = path
	cfg, err := loadFleetConfig(app)
	if err != nil {
		return change, nil, err
	}
	facts := indexFleetFacts(cfg)[foldFleetAlias(alias)]
	for _, host := range cfg.Hosts {
		if host.Name == name && !strings.EqualFold(host.SSHAlias, alias) {
			return change, facts, fmt.Errorf("fleet host name %q is already used by another endpoint", name)
		}
		if strings.EqualFold(host.SSHAlias, alias) && (!host.Managed() || filepath.Clean(host.Origin()) != filepath.Clean(path)) {
			return change, facts, fmt.Errorf("fleet primary configuration already references SSH alias %q", alias)
		}
	}
	desired, err := fleet.RenderManagedFragment(fleet.ManagedHost{Name: name, SSHAlias: alias, RemoteOS: string(remoteOS)})
	if err != nil {
		return change, facts, err
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		if _, validateErr := fleet.ValidateManagedFragment(path); validateErr != nil {
			return change, facts, validateErr
		}
		current, readErr := os.ReadFile(path)
		if readErr != nil {
			return change, facts, readErr
		}
		if bytes.Equal(current, desired) {
			change.Action = "noop"
		} else {
			change.Action = "update"
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return change, facts, statErr
	} else {
		change.Action = "create"
	}
	return change, facts, nil
}

func renderSSHSetupPlan(app *App, document sshSetupDocument) {
	fmt.Fprintf(app.Out, "SSH setup plan for %s (%s)\n", document.Alias, document.AliasClass)
	if document.KeyPlan != nil {
		fmt.Fprintf(app.Out, "  key:       %s %s\n", document.KeyPlan.Action, document.KeyPlan.Operation)
	}
	if document.ManagedPlan != nil {
		fmt.Fprintf(app.Out, "  config:    %s %s\n", document.ManagedPlan.Action, config.Contract(document.ManagedPlan.Path))
	} else if document.AliasClass == "foreign" {
		fmt.Fprintln(app.Out, "  config:    preserve foreign definition")
	} else {
		fmt.Fprintln(app.Out, "  config:    blocked")
	}
	if document.Fleet.Requested {
		fmt.Fprintf(app.Out, "  fleet:     %s %s\n", document.Fleet.Action, config.Contract(document.Fleet.Path))
	}
	if document.DryRun {
		fmt.Fprintln(app.Out, "  remote:    unknown (dry-run never runs OpenSSH or installers)")
	}
}

func finishSSHSetup(app *App, jsonOut bool, document sshSetupDocument, err error) error {
	if jsonOut {
		if encodeErr := writeSSHJSON(app, document); encodeErr != nil {
			return encodeErr
		}
	} else if document.DryRun || document.Status == "planning" || document.Status == "planned" || document.Status == "confirmation_required" || document.Status == "blocked" {
		renderSSHSetupPlan(app, document)
		fmt.Fprintf(app.Out, "  status:    %s\n", document.Status)
	} else {
		fmt.Fprintf(app.Out, "SSH setup %s: %s\n", document.Status, document.Alias)
		if document.BootstrapResult != nil {
			for _, hop := range document.BootstrapResult.Hops {
				fmt.Fprintf(app.Out, "  %-20s %-24s %s\n", hop.Alias, hop.Status, hop.Code)
			}
		}
		if document.Fleet.Requested {
			fmt.Fprintf(app.Out, "  fleet: %s (%s)\n", document.Fleet.Action, config.Contract(document.Fleet.Path))
		}
	}
	return err
}

func setupFailureStatus(document sshSetupDocument) string {
	if document.KeyResult != nil || document.ManagedResult != nil || document.BootstrapResult != nil {
		return "partial"
	}
	return "failed"
}

func newSSHProbeCmd(app *App) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "probe <alias>",
		Short: "Perform one fresh noninteractive ordinary SSH login",
		Long: `Use a fresh connection with sharing disabled and BatchMode enabled.
Host-key and known-hosts policy is preserved; configured KnownHostsCommand,
UpdateHostKeys, resolver, or Match exec behavior may still run.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), sshProbeTimeout)
			defer cancel()
			return runSSHProbe(ctx, app, args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit one versioned JSON object")
	cmd.ValidArgsFunction = completeSSHAliases(app)
	return cmd
}

func runSSHProbe(ctx context.Context, app *App, alias string, jsonOut bool) error {
	if err := sshhost.ValidateLookupAlias(alias); err != nil {
		return asUsageError(err)
	}
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	inventory, discoverErr := service.Discover(ctx)
	if discoverErr != nil {
		result := sshhost.ProbeResult{Alias: alias, Status: sshhost.ProbeError, Code: "discovery_error"}
		document := sshProbeDocument{
			SchemaVersion: sshCLISchemaVersion, Kind: "ssh_probe", Status: "failed",
			Result: result, ErrorCode: sshErrorCode(discoverErr),
		}
		return finishSSHProbe(app, jsonOut, document, discoverErr)
	}
	if selectionErr := requireStaticProbeSelection(alias, inventory); selectionErr != nil {
		result := sshhost.ProbeResult{Alias: alias, Status: sshhost.ProbeNotReady, Code: "static_selection_blocked"}
		document := sshProbeDocument{
			SchemaVersion: sshCLISchemaVersion, Kind: "ssh_probe", Status: "blocked",
			Result: result, ErrorCode: "blocked",
		}
		return finishSSHProbe(app, jsonOut, document, selectionErr)
	}

	result, probeErr := service.Probe(ctx, alias)
	document := sshProbeDocument{SchemaVersion: sshCLISchemaVersion, Kind: "ssh_probe", Status: string(result.Status), Result: result}
	if probeErr != nil {
		document.Status = "failed"
		document.ErrorCode = sshErrorCode(probeErr)
	} else if !result.Ready {
		probeErr = fmt.Errorf("SSH alias %q is not ready", alias)
		document.ErrorCode = "not_ready"
	}
	return finishSSHProbe(app, jsonOut, document, probeErr)
}

func requireStaticProbeSelection(alias string, inventory sshhost.Inventory) error {
	if !inventory.Complete {
		return fmt.Errorf("SSH alias %q cannot be selected because static discovery is incomplete: %w", alias, sshhost.ErrBlocked)
	}
	found, ok := inventory.Find(alias)
	if !ok {
		return fmt.Errorf("SSH alias %q is not a definitely active exact declaration: %w", alias, sshhost.ErrBlocked)
	}
	if found.Conflict || len(found.Definitions) != 1 || aliasStatus(found) != "active" {
		return fmt.Errorf("SSH alias %q is not uniquely selectable (status %s): %w", alias, aliasStatus(found), sshhost.ErrBlocked)
	}
	definition := found.Definitions[0]
	if definition.Reachability != sshhost.Reachable || definition.Ownership == sshhost.OwnershipConflict {
		return fmt.Errorf("SSH alias %q is not definitely active and non-conflicting: %w", alias, sshhost.ErrBlocked)
	}
	return nil
}

func finishSSHProbe(app *App, jsonOut bool, document sshProbeDocument, err error) error {
	if jsonOut {
		if encodeErr := writeSSHJSON(app, document); encodeErr != nil {
			return encodeErr
		}
	} else {
		fmt.Fprintf(app.Out, "%s\t%s\t%s\n", document.Result.Alias, document.Result.Status, document.Result.Code)
	}
	return err
}

func newSSHRemoveCmd(app *App) *cobra.Command {
	var fleetRemove, dryRun, yes, jsonOut bool
	cmd := &cobra.Command{
		Use:   "remove <alias>",
		Short: "Remove only dev-owned SSH and optional fleet fragments",
		Long: `Remove a canonical alias fragment only. Keys, known_hosts, the shared
Include, and remote authorized_keys are never removed. A generated fleet entry
must be explicitly removed first with --fleet.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSSHRemove(cmd.Context(), app, args[0], fleetRemove, dryRun, yes, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&fleetRemove, "fleet", false, "remove the owned fleet fragment first")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "render removal plans without writing")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm removals without prompting")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit one versioned JSON plan or result")
	cmd.ValidArgsFunction = completeSSHAliases(app)
	return cmd
}

func runSSHRemove(ctx context.Context, app *App, alias string, removeFleet, dryRun, yes, jsonOut bool) error {
	if err := sshhost.ValidateManagedAlias(alias); err != nil {
		return asUsageError(err)
	}
	if dryRun {
		return runSSHRemoveOperation(ctx, app, alias, removeFleet, true, yes, jsonOut)
	}
	invoked := false
	err := withSSHOperationLock(ctx, app, func() error {
		invoked = true
		return runSSHRemoveOperation(ctx, app, alias, removeFleet, dryRun, yes, jsonOut)
	})
	if invoked {
		return err
	}
	document := sshRemoveDocument{
		SchemaVersion: sshCLISchemaVersion,
		Kind:          "ssh_remove_result",
		Status:        "failed",
		Alias:         alias,
		DryRun:        dryRun,
		Fleet:         sshFleetChange{Requested: removeFleet, Action: "not_present", SSHAlias: alias},
		ErrorCode:     sshErrorCode(err),
	}
	return finishSSHRemove(app, jsonOut, document, err)
}

func runSSHRemoveOperation(ctx context.Context, app *App, alias string, removeFleet, dryRun, yes, jsonOut bool) error {
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	document := sshRemoveDocument{
		SchemaVersion: sshCLISchemaVersion,
		Kind:          "ssh_remove_result",
		Status:        "planning",
		Alias:         alias,
		DryRun:        dryRun,
		Fleet:         sshFleetChange{Requested: removeFleet, Action: "not_present", SSHAlias: alias},
	}
	if dryRun {
		document.Kind = "ssh_remove_plan"
	}
	if _, inspectErr := service.InspectManaged(alias); inspectErr != nil {
		document.Status = "blocked"
		document.ErrorCode = "not_managed"
		return finishSSHRemove(app, jsonOut, document, fmt.Errorf("SSH alias %q is not a canonical dev-owned fragment: %w", alias, inspectErr))
	}
	plan, planErr := service.PlanRemove(ctx, alias)
	document.Plan = &plan
	if planErr != nil {
		document.Status = "blocked"
		document.ErrorCode = sshErrorCode(planErr)
		return finishSSHRemove(app, jsonOut, document, planErr)
	}
	renderSSHDiagnostics(app, plan.Diagnostics)
	if !plan.Ready() || plan.Action == sshhost.ActionBlocked {
		document.Status = "blocked"
		document.ErrorCode = "managed_plan_blocked"
		return finishSSHRemove(app, jsonOut, document, sshhost.ErrBlocked)
	}

	primaryPath := fleetConfigPath(app)
	fleetPath, fleetPathErr := fleet.ManagedFragmentPath(primaryPath, alias)
	if fleetPathErr != nil {
		document.Status = "blocked"
		document.ErrorCode = sshErrorCode(fleetPathErr)
		return finishSSHRemove(app, jsonOut, document, fleetPathErr)
	}
	document.Fleet.Path = fleetPath
	if fleetLoadErr := ensureNoPrimaryFleetAlias(app, alias); fleetLoadErr != nil {
		document.Status = "blocked"
		document.ErrorCode = fleetReferenceErrorCode(fleetLoadErr)
		return finishSSHRemove(app, jsonOut, document, fleetLoadErr)
	}
	fleetExists := false
	if _, statErr := os.Lstat(fleetPath); statErr == nil {
		if _, validateErr := fleet.ValidateManagedFragment(fleetPath); validateErr != nil {
			document.Status = "blocked"
			document.ErrorCode = sshErrorCode(validateErr)
			return finishSSHRemove(app, jsonOut, document, validateErr)
		}
		fleetExists = true
		document.Fleet.Action = "remove"
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		document.Status = "blocked"
		document.ErrorCode = sshErrorCode(statErr)
		return finishSSHRemove(app, jsonOut, document, statErr)
	}
	if fleetExists && !removeFleet {
		document.Status = "blocked"
		document.ErrorCode = "fleet_removal_required"
		return finishSSHRemove(app, jsonOut, document, errors.New("an owned fleet fragment exists; rerun with --fleet to remove it explicitly"))
	}
	if dryRun {
		document.Status = "planned"
		return finishSSHRemove(app, jsonOut, document, nil)
	}
	if !yes {
		if jsonOut || !app.interactive() {
			document.Status = "confirmation_required"
			document.ErrorCode = "confirmation_required"
			return finishSSHRemove(app, jsonOut, document, errors.New("--yes is required to remove SSH config without an interactive terminal"))
		}
		renderSSHRemovePlan(app, document)
		confirmed, promptErr := newPrompter(app).confirm("Remove these dev-owned fragments?", false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			return errPromptCanceled
		}
	}
	if primaryErr := ensureNoPrimaryFleetAlias(app, alias); primaryErr != nil {
		document.Status = "failed"
		document.ErrorCode = fleetReferenceErrorCode(primaryErr)
		return finishSSHRemove(app, jsonOut, document, primaryErr)
	}
	if removeFleet {
		if _, removeErr := fleet.RemoveManagedFragment(ctx, primaryPath, alias, nil); removeErr != nil {
			document.Status = "failed"
			document.Fleet.Action = "failed"
			document.ErrorCode = sshErrorCode(removeErr)
			return finishSSHRemove(app, jsonOut, document, removeErr)
		}
		if fleetExists {
			document.Fleet.Action = "removed"
		}
	}
	if beforeSSHRemoveFinalCheck != nil {
		beforeSSHRemoveFinalCheck()
	}
	if referenceErr := ensureNoFleetAliasReferences(app, alias); referenceErr != nil {
		document.Status = "blocked"
		if document.Fleet.Action == "removed" {
			document.Status = "partial"
		}
		document.ErrorCode = fleetReferenceErrorCode(referenceErr)
		return finishSSHRemove(app, jsonOut, document, referenceErr)
	}
	result, applyErr := service.ApplyManaged(ctx, plan)
	document.Result = &result
	if applyErr != nil {
		document.Status = "partial"
		document.ErrorCode = sshErrorCode(applyErr)
		return finishSSHRemove(app, jsonOut, document, applyErr)
	}
	document.Status = "removed"
	return finishSSHRemove(app, jsonOut, document, nil)
}

func ensureNoPrimaryFleetAlias(app *App, alias string) error {
	cfg, err := loadFleetConfig(app)
	if err != nil {
		return err
	}
	for _, host := range cfg.Hosts {
		if !host.Managed() && strings.EqualFold(host.SSHAlias, alias) {
			return fmt.Errorf("primary remotes.toml host %q references %q; use dev fleet config edit first: %w", host.Name, alias, errPrimaryFleetReference)
		}
	}
	return nil
}

func ensureNoFleetAliasReferences(app *App, alias string) error {
	cfg, err := loadFleetConfig(app)
	if err != nil {
		return err
	}
	for _, host := range cfg.Hosts {
		if !strings.EqualFold(host.SSHAlias, alias) {
			continue
		}
		if host.Managed() {
			return fmt.Errorf("generated fleet host %q still references %q: %w", host.Name, alias, errGeneratedFleetReference)
		}
		return fmt.Errorf("primary remotes.toml host %q references %q; use dev fleet config edit first: %w", host.Name, alias, errPrimaryFleetReference)
	}
	return nil
}

func fleetReferenceErrorCode(err error) string {
	switch {
	case errors.Is(err, errPrimaryFleetReference):
		return "primary_fleet_reference"
	case errors.Is(err, errGeneratedFleetReference):
		return "fleet_removal_required"
	default:
		return "fleet_config_invalid"
	}
}

func withSSHOperationLock(ctx context.Context, app *App, operation func() error) error {
	service, err := app.sshHosts()
	if err != nil {
		return err
	}
	return sshhost.WithOperationLock(ctx, service.Paths(), operation)
}

func renderSSHRemovePlan(app *App, document sshRemoveDocument) {
	fmt.Fprintf(app.Out, "SSH remove plan for %s\n", document.Alias)
	if document.Plan != nil {
		fmt.Fprintf(app.Out, "  config: %s %s\n", document.Plan.Action, config.Contract(document.Plan.Path))
	}
	if document.Fleet.Action != "not_present" {
		fmt.Fprintf(app.Out, "  fleet:  %s %s\n", document.Fleet.Action, config.Contract(document.Fleet.Path))
	}
}

func finishSSHRemove(app *App, jsonOut bool, document sshRemoveDocument, err error) error {
	if jsonOut {
		if encodeErr := writeSSHJSON(app, document); encodeErr != nil {
			return encodeErr
		}
	} else if document.Result != nil {
		fmt.Fprintf(app.Out, "SSH remove %s: %s\n", document.Status, document.Alias)
	} else {
		renderSSHRemovePlan(app, document)
		fmt.Fprintf(app.Out, "  status: %s\n", document.Status)
	}
	return err
}

func completeSSHAliases(app *App) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, noFileCompletion
		}
		return staticSSHAliasCompletions(cmd.Context(), app, toComplete), noFileCompletion
	}
}

func completeSSHFlagAliases(app *App) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return staticSSHAliasCompletions(cmd.Context(), app, toComplete), noFileCompletion
	}
}

func completeSSHOSOverrides(app *App) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		aliases := staticSSHAliasCompletions(cmd.Context(), app, "")
		out := make([]string, 0, len(aliases)*2)
		for _, described := range aliases {
			alias, _, _ := strings.Cut(described, "\t")
			for _, remoteOS := range []string{fleet.RemoteOSPOSIX, fleet.RemoteOSWindows} {
				candidate := alias + "=" + remoteOS
				if strings.HasPrefix(candidate, toComplete) {
					out = append(out, candidate)
				}
			}
		}
		return out, noFileCompletion
	}
}

func staticSSHAliasCompletions(ctx context.Context, app *App, toComplete string) []string {
	service, err := app.sshHosts()
	if err != nil {
		return nil
	}
	inventory, err := service.Discover(ctx)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(inventory.Aliases))
	for _, alias := range inventory.Aliases {
		out = addCompletion(out, alias.Name, aliasStatus(alias)+" · "+aliasOwnership(alias), toComplete)
	}
	return out
}

func foldFleetAlias(value string) string {
	return strings.Map(func(r rune) rune {
		folded := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < folded {
				folded = next
			}
		}
		return folded
	}, value)
}

func indexFleetFacts(cfg fleet.Config) map[string][]sshFleetFact {
	factsByAlias := make(map[string][]sshFleetFact)
	for _, host := range cfg.Hosts {
		if host.SSHAlias == "" {
			continue
		}
		key := foldFleetAlias(host.SSHAlias)
		factsByAlias[key] = append(factsByAlias[key], sshFleetFact{
			Name:     host.Name,
			SSHAlias: host.SSHAlias,
			RemoteOS: host.EffectiveRemoteOS(),
			Origin:   host.Origin(),
			Managed:  host.Managed(),
		})
	}
	for _, facts := range factsByAlias {
		sort.SliceStable(facts, func(i, j int) bool {
			if facts[i].Name == facts[j].Name {
				return facts[i].Origin < facts[j].Origin
			}
			return facts[i].Name < facts[j].Name
		})
	}
	return factsByAlias
}

func renderSSHDiagnostics(app *App, diagnostics []sshhost.Diagnostic) {
	for _, diagnostic := range diagnostics {
		message := diagnostic.Message
		if message == "" {
			message = diagnostic.Code
		}
		fmt.Fprintf(app.Err, "dev: warning: SSH %s: %s\n", diagnostic.Code, message)
	}
}

func hasSSHDiagnostic(diagnostics []sshhost.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func sshErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, sshhost.ErrInteractionRequired):
		return "interaction_required"
	case errors.Is(err, sshhost.ErrInvalidAlias):
		return "invalid_alias"
	case errors.Is(err, sshhost.ErrNotManaged):
		return "not_managed"
	case errors.Is(err, sshhost.ErrBlocked):
		return "blocked"
	case errors.Is(err, sshhost.ErrSourceChanged):
		return "source_changed"
	case errors.Is(err, sshhost.ErrUnsafePath):
		return "unsafe_path"
	case errors.Is(err, fleet.ErrManagedFragmentConflict):
		return "fleet_fragment_conflict"
	default:
		return "operation_failed"
	}
}

func writeSSHJSON(app *App, value any) error {
	encoder := json.NewEncoder(app.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
