package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type sshTUIOnboardingPlan struct {
	prepared     *sshPreparedOnboarding
	preview      sshflow.OnboardPreview
	registryPath string
	fleetPath    string
	used         atomic.Bool
}

func (p *sshTUIOnboardingPlan) Preview() sshflow.OnboardPreview {
	copy := p.preview
	copy.Targets = append([]sshflow.OnboardTarget(nil), copy.Targets...)
	copy.Notes = append([]string(nil), copy.Notes...)
	copy.PreserveAliases = append([]string(nil), copy.PreserveAliases...)
	copy.Init.Diagnostics = append([]sshhost.Diagnostic(nil), copy.Init.Diagnostics...)
	return copy
}

func prepareSSHTUIOnboarding(ctx context.Context, app *App, request sshflow.OnboardRequest) (tui.SSHOnboardingPlan, error) {
	if request.Profile != nil {
		if request.Alias != request.Profile.Alias || request.Auth != "key" || request.KeyPath == "" && request.KeyFingerprint == "" || request.Candidate != nil {
			return nil, errors.New("choose a key for the selected SSH profile")
		}
		if err := revalidateSSHTUIProfile(ctx, app, *request.Profile); err != nil {
			return nil, err
		}
	} else if request.Alias == "" || request.HostName == "" || request.User == "" || request.Port < 1 || request.Port > 65535 {
		return nil, errors.New("enter an SSH alias, host, remote user and port 1–65535")
	}
	if request.Auth != "config" && request.Auth != "existing" && request.Auth != "key" {
		return nil, errors.New("choose configure only, existing authentication or explicit key bootstrap")
	}
	if request.Auth != "key" && (request.KeyPath != "" || request.GenerateKey || request.KeyFingerprint != "") {
		return nil, errors.New("key selection requires key bootstrap")
	}
	if request.Auth == "key" && request.KeyPath == "" && request.KeyFingerprint == "" {
		return nil, errors.New("select an existing key or a new key path")
	}
	if request.KeyFingerprint != "" && (request.KeyPath != "" || request.GenerateKey || request.KeyAgentSocket == "") {
		return nil, errors.New("choose either an agent key or a key path")
	}
	if request.Auth == "config" && request.To != "" {
		return nil, errors.New("registration requires existing authentication or explicit key bootstrap")
	}
	notes := []string{}
	var candidate *sshdiscovery.Candidate
	if request.Candidate != nil {
		copy := *request.Candidate
		copy.Addresses = append([]string(nil), copy.Addresses...)
		candidate = &copy
		if copy.Source != sshdiscovery.SourceLAN && copy.Source != sshdiscovery.SourceTailscale || copy.ID == "" || copy.Scope == "" || copy.NativeID == "" {
			return nil, errors.New("select an exact LAN or Tailscale observation")
		}
		if !sshCandidateAddressMatches(request.HostName, copy) {
			return nil, errors.New("host differs from the selected discovery endpoint; choose a matching address or set up a manual connection")
		}
		if request.Report != nil {
			report := request.Report
			matched := false
			for _, observed := range report.Candidates {
				if reflect.DeepEqual(observed, copy) {
					matched = true
					break
				}
			}
			if report.Source != copy.Source || report.Scope != copy.Scope || !matched {
				return nil, errors.New("discovery selection does not match its source report")
			}
			notes = append(notes, fmt.Sprintf("%s observation from %s (%s)", copy.Source, report.ObservedAt.Format(time.RFC3339), report.Status))
			if report.Stale || nowSSH().Sub(report.ObservedAt) > sshdiscovery.CacheTTL || !report.Complete {
				notes = append(notes, "This is an old or incomplete observation; it does not prove current connectivity or authentication.")
			}
		} else {
			notes = append(notes, "Historical discovery observation; freshness is unavailable and no authentication is implied.")
		}
		if copy.Source == sshdiscovery.SourceLAN {
			current, err := app.sshDiscovery().CurrentLANScope(copy.Scope)
			if err != nil || !current {
				return nil, errors.New("selected LAN interface or scope changed; scan that range again or enter a manual connection")
			}
		}
	}
	copy := *app
	copy.Out, copy.Err, copy.In = io.Discard, io.Discard, strings.NewReader("")
	options := sshSetupOptions{json: true, yes: true, machineID: request.MachineID, to: request.To, targetOS: request.RemoteOS, fleetName: request.FleetName, herdrLabel: request.HerdrLabel, herdrSession: request.HerdrSession}
	if request.Profile == nil {
		options.hostName, options.hostNameChanged, options.user, options.userChanged = request.HostName, true, request.User, true
		options.port, options.portChanged, options.connectionChanged = request.Port, true, true
	}
	if options.herdrSession == "" {
		options.herdrSession = "default"
	}
	switch request.Auth {
	case "config":
		options.configOnly = true
	case "existing":
		options.auth = "existing"
	case "key":
		service, err := copy.sshHosts()
		if err != nil {
			return nil, err
		}
		keyRequest := sshhost.KeyRequest{Operation: sshhost.KeyUse, Path: request.KeyPath, AllowDerive: true}
		options.key = request.KeyPath
		if request.KeyFingerprint != "" {
			candidate, err := selectSSHTUIAgentKey(ctx, service, request)
			if err != nil {
				return nil, err
			}
			options.key, options.keyCandidate = "", &candidate
			keyRequest = sshhost.KeyRequest{Candidate: candidate}
		}
		if request.GenerateKey {
			destination := normalizeSSHKeyPromptPath(service.Paths().SSHDir, request.KeyPath)
			keyRequest = sshhost.KeyRequest{Operation: sshhost.KeyGenerate, DestinationIdentity: destination, Comment: request.KeyComment, Interactive: true}
			options.key, options.generateKey, options.keyPath, options.comment = "", true, destination, request.KeyComment
		}
		keyPlan, err := service.PlanKey(ctx, keyRequest)
		if err != nil {
			return nil, err
		}
		if !keyPlan.Ready() {
			return nil, sshKeyPlanBlockedError(keyPlan)
		}
		options.keyPlan = &keyPlan
		identity := keyPlan.IdentityFile
		if keyPlan.Agent != nil {
			identity = sshhost.AgentProviderLabel(keyPlan.Agent.Provider) + " agent"
		}
		notes = append(notes, fmt.Sprintf("Key: %s %s %s", keyPlan.Operation, identity, keyPlan.Fingerprint))
		if keyPlan.CreateParent != "" {
			notes = append(notes, "Create key directory "+keyPlan.CreateParent+" (mode 0700).")
		}
	}
	item, err := prepareSSHOnboardItem(ctx, &copy, request.Alias, candidate, options)
	if err != nil {
		return nil, err
	}
	prepared, err := planSSHOnboardItems(ctx, &copy, []sshOnboardItem{item}, false)
	if err != nil {
		return nil, err
	}
	prepared.native = true
	if candidate != nil && candidate.Source == sshdiscovery.SourceLAN {
		prepared.lanScope = candidate.Scope
	}
	if request.To == "herdr" || request.To == "both" {
		notes = append(notes, "Herdr may install/start its remote server; native approvals remain interactive.")
	}
	notes = append(notes, sshAgentPreview(item)...)
	if item.Definition != nil {
		notes = append(notes, "Create or update the displayed SSH configuration and explicit machine mappings.")
	} else {
		notes = append(notes, "Preserve the existing foreign SSH configuration; record explicit machine mappings.")
	}
	preview := sshflow.OnboardPreview{Targets: append([]sshflow.OnboardTarget(nil), prepared.plan.Targets...), Init: prepared.init, Notes: notes}
	if item.Definition == nil {
		preview.PreserveAliases = []string{request.Alias}
	}
	return &sshTUIOnboardingPlan{prepared: prepared, registryPath: app.machineStore().Path, fleetPath: fleetConfigPath(app), preview: preview}, nil
}

// selectSSHTUIAgentKey re-catalogs the reviewed agent socket and requires the
// same fingerprint before planning.
func selectSSHTUIAgentKey(ctx context.Context, service *sshhost.Service, request sshflow.OnboardRequest) (sshhost.KeyCandidate, error) {
	ref := sshhost.AgentSocketRef{Provider: sshhost.AgentProviderCustom, Socket: request.KeyAgentSocket}
	for _, observation := range service.ObserveAgentProviders(runtime.GOOS) {
		if observation.SocketPresent && observation.Socket == request.KeyAgentSocket {
			ref.Provider = observation.Provider
		}
	}
	candidate, err := selectSSHAgentKey(ctx, service, ref, request.KeyFingerprint)
	if err != nil {
		return candidate, fmt.Errorf("%w; list keys again: %w", err, sshhost.ErrSourceChanged)
	}
	return candidate, nil
}

// listSSHTUIKeys offers file-backed local keys, named and ambient agent keys,
// and a generated destination. The ambient socket is captured before catalog
// enumeration and remains an explicit reference after selection.
func listSSHTUIKeys(ctx context.Context, app *App, alias string) ([]tui.SSHKeyChoice, error) {
	service, err := app.sshHosts()
	if err != nil {
		return nil, err
	}
	agents := sshPickerAgents(nil, service, sshSetupOptions{})
	ambientUnavailable := ""
	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		var ref sshhost.AgentSocketRef
		err := sshhost.ValidateAgentSocketPath(socket)
		if err == nil {
			ref, err = service.ResolveAgentSocket(runtime.GOOS, socket)
		}
		if err != nil {
			ambientUnavailable = "SSH_AUTH_SOCK is not a usable owned socket; select another agent or configure an absolute socket path, then reopen the picker."
		} else {
			known := false
			for _, agent := range agents {
				known = known || agent.Socket == ref.Socket
			}
			if !known {
				agents = append(agents, ref)
			}
		}
	}
	catalog, err := localSSHKeyCatalog(ctx, service, agents...)
	if err != nil {
		return nil, err
	}
	home := service.Paths().Home
	choices := make([]tui.SSHKeyChoice, 0, len(catalog.Candidates)+1)
	if ambientUnavailable != "" {
		choices = append(choices, tui.SSHKeyChoice{Label: "SSH_AUTH_SOCK agent (unavailable)", Description: ambientUnavailable, UnavailableReason: ambientUnavailable})
	}
	for _, key := range catalog.Candidates {
		if key.Agent != nil {
			label := sshKeyLabel(home, key)
			if key.Comment != "" {
				label += ": " + key.Comment
			}
			choices = append(choices, tui.SSHKeyChoice{Label: label, Description: key.Algorithm + " · " + key.Fingerprint, Fingerprint: key.Fingerprint, AgentProvider: string(key.Agent.Provider), AgentSocket: key.Agent.Socket})
			continue
		}
		path := key.IdentityFile
		if path == "" {
			path = key.PublicPath
		}
		if path != "" {
			choices = append(choices, tui.SSHKeyChoice{Label: sshKeyLabel(home, key), Description: key.Algorithm + " · " + sshKeySigner(key), Path: path})
		}
	}
	for _, observation := range service.ObserveAgentProviders(runtime.GOOS) {
		if observation.Provider == sshhost.AgentProviderWindowsPipe || observation.SocketPresent {
			continue
		}
		hint := "The SSH agent socket was not found; " + sshhost.AgentProviderEnableHint(observation.Provider) + ", then reopen the key picker."
		choices = append(choices, tui.SSHKeyChoice{Label: observation.Label + " agent (unavailable)", Description: hint, AgentProvider: string(observation.Provider), UnavailableReason: hint})
	}
	if sshhost.ValidateLookupAlias(alias) != nil {
		alias = "host"
	}
	destination := filepath.Join(service.Paths().SSHDir, "id_ed25519_dev_"+alias)
	return append(choices, tui.SSHKeyChoice{Label: "+ Generate a new key", Description: "Ed25519 at " + sshKeyLabel(home, sshhost.KeyCandidate{IdentityFile: destination}), Path: destination, Generate: true}), nil
}

func (w *sshTUIWorkflow) applyOnboarding() error {
	plan, ok := w.request.Onboarding.(*sshTUIOnboardingPlan)
	if !ok || plan == nil || plan.prepared == nil {
		return errors.New("prepare and review an SSH onboarding plan first")
	}
	if plan.used.Swap(true) {
		return errors.New("this SSH setup plan was already attempted; refresh and review a new plan")
	}
	if w.app.machineStore().Path != plan.registryPath || fleetConfigPath(&w.app) != plan.fleetPath {
		return errors.New("SSH setup configuration changed; review a new plan")
	}
	service, err := w.app.sshHosts()
	if err != nil {
		return err
	}
	if service.Paths() != plan.prepared.service.Paths() {
		return errors.New("SSH configuration root changed; review a new plan")
	}
	result, err := applySSHOnboardPlan(w.ctx, &w.app, plan.prepared, false)
	w.result.Onboarding = &result
	w.result.Status = "SSH setup: " + result.Status
	for _, registration := range result.Registrations {
		for _, outcome := range registration.Outcomes {
			if outcome.Status == "applied" || outcome.Status == "unknown" {
				w.result.MembershipChanged = true
			}
		}
	}
	return err
}
