package sshhost

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// PlanBootstrap is side-effect-free. Unless a previously resolved Route is
// supplied, route/key/network work is represented honestly as unknown.
func (s *Service) PlanBootstrap(request BootstrapRequest) (BootstrapPlan, error) {
	alias := request.Alias
	plan := BootstrapPlan{
		Alias: alias, Status: BootstrapUnknown,
		Steps: []BootstrapStep{
			{Code: "key_material", State: BootstrapUnknown},
			{Code: "route_resolution", State: BootstrapUnknown},
			{Code: "ordinary_probes", State: BootstrapUnknown},
			{Code: "key_installation", State: BootstrapUnknown},
			{Code: "exact_key_verification", State: BootstrapUnknown},
			{Code: "ordinary_alias_gate", State: BootstrapUnknown},
		},
	}
	if request.Route.state != nil {
		state, err := s.validateRoute(request.Route)
		if err != nil {
			return BootstrapPlan{}, err
		}
		if alias != "" && !equalAlias(alias, state.safe.Alias) {
			return BootstrapPlan{}, errors.New("bootstrap alias does not match supplied route")
		}
		alias = state.safe.Alias
		plan.Alias = alias
		plan.RouteKnown = true
		plan.Hops = append([]RouteHop(nil), state.safe.Hops...)
	}
	if err := validateRouteLookupAlias(alias); err != nil {
		return BootstrapPlan{}, err
	}
	return plan, nil
}

// Bootstrap processes route hops outermost-first. Remote command failures are
// returned as a resumable partial result; caller/input errors and context
// cancellation are returned as Go errors alongside any completed hop state.
func (s *Service) Bootstrap(ctx context.Context, request BootstrapRequest) (BootstrapResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return BootstrapResult{}, err
	}
	material, err := s.bootstrapKeyMaterial(ctx, request)
	if err != nil {
		return BootstrapResult{}, err
	}
	route, routeState, err := s.bootstrapRoute(ctx, request)
	if err != nil {
		return BootstrapResult{}, err
	}
	result := BootstrapResult{
		Alias: route.Alias, Status: BootstrapPartial, Code: "partial", Partial: true,
		TargetRemoteOS: route.TargetRemoteOS,
	}
	for _, hop := range route.Hops {
		result.Hops = append(result.Hops, HopResult{
			Alias: hop.Alias, Reference: hop.Reference, RemoteOS: hop.RemoteOS,
			AdminState: hop.AdminState, Target: hop.Target, Status: HopNotAttempted,
		})
	}
	selector, cleanup, err := s.prepareKeySelector(material)
	if err != nil {
		return result, err
	}
	defer cleanup()

	for index, hopState := range routeState.hops {
		hop := &result.Hops[index]

		ordinary, probeErr := s.runSSHProof(ctx, hopState, keySelector{}, false)
		if probeErr != nil {
			hop.Status = HopFailed
			hop.Code = "ordinary_probe_error"
			if ctxErr := ctx.Err(); ctxErr != nil {
				return finalizeBootstrap(result), ctxErr
			}
			return finalizeBootstrap(result), nil
		}
		hop.OrdinaryBefore = ordinary

		exact, exactErr := s.runSSHProof(ctx, hopState, selector, true)
		if exactErr != nil {
			hop.Status = HopFailed
			hop.Code = "exact_probe_error"
			if ctxErr := ctx.Err(); ctxErr != nil {
				return finalizeBootstrap(result), ctxErr
			}
			return finalizeBootstrap(result), nil
		}
		if exact {
			hop.Present = true
			hop.Verified = true
			hop.Status = HopPresent
			hop.Code = "key_present"
			if ready, gateErr := s.runOrdinaryBootstrapGate(ctx, hopState, hop, HopFailed); !ready {
				return finalizeBootstrap(result), gateErr
			}
			continue
		}

		if ordinary && !hop.Target && !request.InstallOnWorkingJump {
			hop.Skipped = true
			hop.Status = HopWorkingSkipped
			hop.Code = "working_jump_skipped"
			if ready, gateErr := s.runOrdinaryBootstrapGate(ctx, hopState, hop, HopFailed); !ready {
				return finalizeBootstrap(result), gateErr
			}
			continue
		}

		if hop.RemoteOS == RemoteOSUnknown {
			hop.Status = HopManual
			hop.Code = "remote_os_required"
			return finalizeBootstrap(result), nil
		}
		administrator := false
		if hop.RemoteOS == RemoteOSWindows {
			admin, adminErr := s.detectWindowsAdministrator(ctx, hopState, request.Interactive)
			if adminErr != nil {
				hop.Status = HopManual
				if !request.Interactive {
					hop.Code = "interaction_required"
				} else {
					hop.Code = "windows_admin_detection_failed"
				}
				if ctxErr := ctx.Err(); ctxErr != nil {
					return finalizeBootstrap(result), ctxErr
				}
				return finalizeBootstrap(result), nil
			}
			administrator = admin
			if administrator {
				hop.AdminState = AdminAdministrator
				if !request.AllowWindowsAdminAuthorizedKeys {
					hop.Status = HopManual
					hop.Code = "windows_admin_consent_required"
					return finalizeBootstrap(result), nil
				}
			} else {
				hop.AdminState = AdminStandard
			}
		}

		program := posixInstallerProgram()
		if hop.RemoteOS == RemoteOSWindows {
			program = windowsInstallerProgram(administrator)
		}
		installResult, installErr := s.runHopSSH(
			ctx, hopState, !request.Interactive, program,
			append(append([]byte(nil), material.publicLine...), '\n'), request.Interactive,
			"ssh public-key installer",
		)
		if installErr != nil || installResult.ExitCode != 0 {
			hop.Status = HopUnknown
			hop.Unknown = true
			switch {
			case hop.RemoteOS == RemoteOSWindows && administrator && installErr == nil:
				hop.Code = "windows_elevation_required_install_unknown"
			case !request.Interactive:
				hop.Code = "interaction_required_install_unknown"
			default:
				hop.Code = "install_state_unknown"
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return finalizeBootstrap(result), ctxErr
			}
			return finalizeBootstrap(result), nil
		}
		hop.Installed = true

		verified, verifyErr := s.runSSHProof(ctx, hopState, selector, true)
		if verifyErr != nil {
			hop.Status = HopFailed
			hop.Code = "exact_verification_error"
			if ctxErr := ctx.Err(); ctxErr != nil {
				return finalizeBootstrap(result), ctxErr
			}
			return finalizeBootstrap(result), nil
		}
		if !verified {
			hop.Status = HopManual
			hop.Code = "exact_verification_failed"
			return finalizeBootstrap(result), nil
		}
		hop.Present = true
		hop.Verified = true

		if ready, gateErr := s.runOrdinaryBootstrapGate(ctx, hopState, hop, HopManual); !ready {
			return finalizeBootstrap(result), gateErr
		}
		hop.Status = HopInstalled
		hop.Code = "installed_and_verified"
	}
	return finalizeBootstrap(result), nil
}

func (s *Service) runOrdinaryBootstrapGate(ctx context.Context, hopState routeHopState, hop *HopResult, failedStatus HopStatus) (bool, error) {
	gate, err := s.runSSHProof(ctx, hopState, keySelector{}, false)
	if err != nil {
		hop.Status = HopFailed
		hop.Code = "ordinary_gate_error"
		return false, ctx.Err()
	}
	hop.OrdinaryReady = gate
	hop.Ready = gate
	if !gate {
		hop.Status = failedStatus
		hop.Code = "ordinary_gate_failed"
		return false, nil
	}
	return true, nil
}

func (s *Service) bootstrapKeyMaterial(ctx context.Context, request BootstrapRequest) (*keyMaterialState, error) {
	if request.Key.Candidate.state != nil && request.Candidate.state != nil {
		return nil, errors.New("bootstrap request cannot select both Key and Candidate")
	}
	var candidate KeyCandidate
	switch {
	case request.Key.Candidate.state != nil:
		candidate = request.Key.Candidate
	case request.Candidate.state != nil:
		candidate = request.Candidate
	default:
		return nil, errors.New("bootstrap requires a selected key result or catalog candidate")
	}
	material, err := s.validateKeyCandidate(candidate)
	if err != nil {
		return nil, err
	}
	verification, err := s.verifyKeyPair(ctx, material, request.Interactive)
	if err != nil {
		return nil, err
	}
	copy := *material
	copy.pairVerification = cloneKeyPairVerification(verification)
	return &copy, nil
}

func (s *Service) bootstrapRoute(ctx context.Context, request BootstrapRequest) (Route, *routeState, error) {
	if request.Route.state != nil {
		state, err := s.validateRoute(request.Route)
		if err != nil {
			return Route{}, nil, err
		}
		if request.Alias != "" && !equalAlias(request.Alias, state.safe.Alias) {
			return Route{}, nil, errors.New("bootstrap alias does not match supplied route")
		}
		if request.TargetRemoteOS != "" && request.TargetRemoteOS != RemoteOSUnknown && request.TargetRemoteOS != state.safe.TargetRemoteOS {
			return Route{}, nil, errors.New("bootstrap target OS does not match supplied route")
		}
		if state.revalidate {
			fresh, err := s.ResolveRoute(ctx, state.request)
			if err != nil {
				return Route{}, nil, err
			}
			freshState, err := s.validateRoute(fresh)
			if err != nil {
				return Route{}, nil, err
			}
			if !routesEqual(fresh, state.safe) {
				return Route{}, nil, fmt.Errorf("effective SSH route changed before bootstrap: %w", ErrSourceChanged)
			}
			return fresh, freshState, nil
		}
		return request.Route, state, nil
	}
	if err := validateRouteLookupAlias(request.Alias); err != nil {
		return Route{}, nil, err
	}
	route, err := s.ResolveRoute(ctx, RouteRequest{
		Alias: request.Alias, TargetRemoteOS: request.TargetRemoteOS, OSOverrides: request.OSOverrides,
	})
	if err != nil {
		return Route{}, nil, err
	}
	state, err := s.validateRoute(route)
	return route, state, err
}

type keySelector struct {
	identity    string
	certificate string
}

func (s *Service) prepareKeySelector(material *keyMaterialState) (keySelector, func(), error) {
	emptyCleanup := func() {}
	if material == nil {
		return keySelector{}, emptyCleanup, errors.New("selected key has no material state")
	}
	safe := material.safe
	record, err := parsePublicKeyRecord(material.publicLine)
	if err != nil || record.metadata.Fingerprint != safe.Fingerprint {
		return keySelector{}, emptyCleanup, errors.New("selected public key material changed")
	}
	certificate := strings.HasSuffix(record.metadata.Algorithm, "-cert-v01@openssh.com")
	backedByIdentity := (safe.Provenance.Private || safe.Provenance.SecurityKeyStub) && safe.IdentityFile != ""
	if backedByIdentity {
		identity, inspectErr := s.inspectSecureIdentity(safe.IdentityFile)
		if inspectErr != nil {
			return keySelector{}, emptyCleanup, fmt.Errorf("validate selected identity: %w", inspectErr)
		}
		if material.pairVerification == nil || material.pairVerification.identity.path != identity.path ||
			!stableFileInfo(material.pairVerification.identity.info, identity.info) {
			return keySelector{}, emptyCleanup, fmt.Errorf("selected identity changed after key-pair proof: %w", ErrSourceChanged)
		}
		if safe.PublicPath != "" {
			companion, readErr := s.readPublicKeyFile(safe.PublicPath)
			if readErr != nil || companion.metadata.Fingerprint != safe.Fingerprint || !publicLinesEqual(companion.normalized, material.publicLine) {
				return keySelector{}, emptyCleanup, errors.New("selected public companion changed")
			}
		}
		selector := keySelector{identity: safe.IdentityFile}
		if certificate {
			selector.certificate = safe.PublicPath
			if selector.certificate == "" {
				return keySelector{}, emptyCleanup, errors.New("selected certificate has no validated certificate file")
			}
		}
		return selector, emptyCleanup, nil
	}

	var stagedFiles []*stagedFile
	cleanup := func() {
		for index := len(stagedFiles) - 1; index >= 0; index-- {
			_ = stagedFiles[index].discard()
		}
	}
	stage := func(line []byte) (string, error) {
		if err := ensurePrivateChild(s.paths.Home, ".ssh", false); err != nil {
			return "", err
		}
		staged, stageErr := createStagedFile(s.paths.SSHDir, append(append([]byte(nil), line...), '\n'), nil)
		if stageErr != nil {
			return "", stageErr
		}
		stagedFiles = append(stagedFiles, staged)
		return filepath.Join(staged.dir, staged.name), nil
	}

	if certificate {
		base, baseErr := basePublicKeyRecord(record)
		if baseErr != nil {
			return keySelector{}, emptyCleanup, baseErr
		}
		identity, stageErr := stage(base.normalized)
		if stageErr != nil {
			cleanup()
			return keySelector{}, emptyCleanup, stageErr
		}
		certificatePath := safe.PublicPath
		if certificatePath != "" {
			current, readErr := s.readPublicKeyFile(certificatePath)
			if readErr != nil || current.metadata.Fingerprint != safe.Fingerprint || !publicLinesEqual(current.normalized, material.publicLine) {
				cleanup()
				return keySelector{}, emptyCleanup, errors.New("selected certificate changed")
			}
		} else {
			certificatePath, stageErr = stage(record.normalized)
			if stageErr != nil {
				cleanup()
				return keySelector{}, emptyCleanup, stageErr
			}
		}
		return keySelector{identity: identity, certificate: certificatePath}, cleanup, nil
	}

	if safe.PublicPath != "" {
		current, readErr := s.readPublicKeyFile(safe.PublicPath)
		if readErr != nil || current.metadata.Fingerprint != safe.Fingerprint || !publicLinesEqual(current.normalized, material.publicLine) {
			return keySelector{}, emptyCleanup, errors.New("selected public key changed")
		}
		return keySelector{identity: safe.PublicPath}, emptyCleanup, nil
	}
	identity, err := stage(record.normalized)
	if err != nil {
		cleanup()
		return keySelector{}, emptyCleanup, err
	}
	return keySelector{identity: identity}, cleanup, nil
}

const (
	sshConnectTimeout        = "15"
	sshServerAliveInterval   = "30"
	sshServerAliveCountMax   = "3"
	exactProofConfigPreamble = "# private dev-cli exact-key proof configuration\n"
)

func appendFreshSSHOptions(args []string, batch bool) []string {
	args = append(args,
		"-S", "none",
		"-o", "ConnectTimeout="+sshConnectTimeout,
		"-o", "ServerAliveInterval="+sshServerAliveInterval,
		"-o", "ServerAliveCountMax="+sshServerAliveCountMax,
	)
	if batch {
		args = append(args, "-o", "BatchMode=yes")
	}
	return args
}

func (s *Service) runSSHProof(ctx context.Context, hop routeHopState, selector keySelector, exact bool) (bool, error) {
	if !exact {
		result, err := s.runHopSSH(ctx, hop, true, "exit 0", nil, false, "ssh authentication proof")
		if err != nil {
			return false, err
		}
		return result.ExitCode == 0, nil
	}
	if selector.identity == "" {
		return false, errors.New("exact SSH proof requires an identity selector")
	}
	configPath, destination, cleanup, err := s.prepareExactProofConfig(hop, selector)
	if err != nil {
		return false, err
	}
	defer cleanup()
	args := appendFreshSSHOptions([]string{"-F", configPath}, true)
	args = append(args, destination, "exit 0")
	result, err := s.runner.Run(ctx, RunRequest{
		Name: "ssh", Args: args, Display: "ssh selected-key-only authentication proof",
	})
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

func (s *Service) prepareExactProofConfig(hop routeHopState, selector keySelector) (string, string, func(), error) {
	route := append([]proofRouteHop(nil), hop.proofRoute...)
	if len(route) == 0 {
		hostName := hop.safe.HostName
		if hostName == "" {
			hostName = hop.destination
		}
		route = []proofRouteHop{{
			alias: hop.destination, reference: hop.safe.Reference, hostName: hostName,
			user: hop.safe.User, port: hop.safe.Port, effective: cloneEffective(hop.effective),
		}}
	}
	content, destination, err := renderExactProofConfig(route, selector)
	if err != nil {
		return "", "", func() {}, err
	}
	staged, err := createStagedFile(s.paths.SSHDir, content, nil)
	if err != nil {
		return "", "", func() {}, err
	}
	return filepath.Join(staged.dir, staged.name), destination, func() { _ = staged.discard() }, nil
}

func renderExactProofConfig(route []proofRouteHop, selector keySelector) ([]byte, string, error) {
	if len(route) == 0 || selector.identity == "" {
		return nil, "", errors.New("exact SSH proof has no route or identity")
	}
	seen := make(map[string]struct{}, len(route))
	for index, hop := range route {
		if err := validateRouteLookupAlias(hop.alias); err != nil {
			return nil, "", err
		}
		key := foldAlias(hop.alias)
		if _, exists := seen[key]; exists {
			return nil, "", fmt.Errorf("exact SSH proof route repeats %q: %w", hop.alias, ErrUnsupportedRoute)
		}
		seen[key] = struct{}{}
		if index < len(route)-1 {
			reference := hop.reference
			if reference == "" {
				reference = hop.alias
			}
			spec, err := parseJumpSpec(reference)
			if err != nil || !equalAlias(spec.host, hop.alias) {
				return nil, "", fmt.Errorf("exact SSH proof has an ambiguous ProxyJump reference %q for %q: %w", reference, hop.alias, ErrUnsupportedRoute)
			}
		}
	}
	destination := "dev-cli-exact-selected-key"
	for suffix := 2; ; suffix++ {
		if _, exists := seen[foldAlias(destination)]; !exists {
			break
		}
		destination = "dev-cli-exact-selected-key-" + strconv.Itoa(suffix)
	}

	var body strings.Builder
	body.WriteString(exactProofConfigPreamble)
	for index, hop := range route {
		hostName := hop.hostName
		if hostName == "" {
			hostName = hop.alias
		}
		if !validUTF8NoControl(hostName) || !validUTF8NoControl(hop.user) || hop.port < 0 || hop.port > 65535 {
			return nil, "", fmt.Errorf("exact SSH proof endpoint %q is unsafe: %w", hop.alias, ErrUnsupportedRoute)
		}
		configAlias := hop.alias
		if index == len(route)-1 {
			configAlias = destination
		}
		body.WriteString("Host ")
		body.WriteString(quoteConfigValue(configAlias))
		body.WriteByte('\n')
		writeConfigDirective(&body, "HostName", hostName)
		writeConfigDirective(&body, "User", hop.user)
		if hop.port != 0 {
			writeConfigDirective(&body, "Port", strconv.Itoa(hop.port))
		}
		writeConfigDirective(&body, "ConnectTimeout", sshConnectTimeout)
		writeConfigDirective(&body, "ServerAliveInterval", sshServerAliveInterval)
		writeConfigDirective(&body, "ServerAliveCountMax", sshServerAliveCountMax)
		writeConfigDirective(&body, "BatchMode", "yes")
		writeConfigDirective(&body, "ControlMaster", "no")
		writeConfigDirective(&body, "ControlPath", "none")
		writeConfigDirective(&body, "ControlPersist", "no")
		if err := writeExactHostKeyPolicy(&body, hop.effective, index == len(route)-1); err != nil {
			return nil, "", fmt.Errorf("preserve host-key policy for %q: %w", hop.alias, err)
		}
		if index == len(route)-1 {
			if index > 0 {
				references := make([]string, 0, index)
				for _, proxy := range route[:index] {
					reference := proxy.reference
					if reference == "" {
						reference = proxy.alias
					}
					references = append(references, reference)
				}
				writeConfigDirective(&body, "ProxyJump", strings.Join(references, ","))
			}
			if err := writeSelectedAuthentication(&body, hop.effective, selector); err != nil {
				return nil, "", err
			}
		} else if err := writeOrdinaryProxyAuthentication(&body, hop.effective); err != nil {
			return nil, "", fmt.Errorf("preserve proxy authentication for %q: %w", hop.alias, err)
		}
	}
	return []byte(body.String()), destination, nil
}

func writeExactHostKeyPolicy(body *strings.Builder, effective EffectiveConfig, syntheticEndpoint bool) error {
	strict := firstEffectiveValue(effective, "stricthostkeychecking")
	if strict == "" {
		strict = "yes"
	}
	writeConfigDirective(body, "StrictHostKeyChecking", strict)
	for _, option := range []struct {
		key       string
		directive string
	}{
		{key: "addressfamily", directive: "AddressFamily"},
		{key: "ciphers", directive: "Ciphers"},
		{key: "macs", directive: "MACs"},
		{key: "kexalgorithms", directive: "KexAlgorithms"},
		{key: "checkhostip", directive: "CheckHostIP"},
		{key: "hashknownhosts", directive: "HashKnownHosts"},
		{key: "hostkeyalias", directive: "HostKeyAlias"},
		{key: "verifyhostkeydns", directive: "VerifyHostKeyDNS"},
		{key: "updatehostkeys", directive: "UpdateHostKeys"},
		{key: "nohostauthenticationforlocalhost", directive: "NoHostAuthenticationForLocalhost"},
		{key: "revokedhostkeys", directive: "RevokedHostKeys"},
		{key: "hostkeyalgorithms", directive: "HostKeyAlgorithms"},
		{key: "casignaturealgorithms", directive: "CASignatureAlgorithms"},
		{key: "requiredrsasize", directive: "RequiredRSASize"},
	} {
		if value := firstEffectiveValue(effective, option.key); value != "" {
			if !validUTF8NoControl(value) || syntheticEndpoint && option.key == "hostkeyalias" && strings.Contains(value, "%") {
				return ErrUnsupportedRoute
			}
			writeConfigDirective(body, option.directive, value)
		}
	}
	if command := firstEffectiveValue(effective, "knownhostscommand"); command != "" && !strings.EqualFold(command, "none") {
		return fmt.Errorf("KnownHostsCommand cannot be replayed safely: %w", ErrUnsupportedRoute)
	}
	for _, option := range []struct {
		key       string
		directive string
	}{
		{key: "globalknownhostsfile", directive: "GlobalKnownHostsFile"},
		{key: "userknownhostsfile", directive: "UserKnownHostsFile"},
	} {
		value := firstEffectiveValue(effective, option.key)
		if value == "" {
			value = "none"
		}
		paths, err := splitEvaluatedKnownHosts(value)
		if err != nil {
			return err
		}
		body.WriteString("    ")
		body.WriteString(option.directive)
		for _, path := range paths {
			body.WriteByte(' ')
			body.WriteString(quoteConfigValue(path))
		}
		body.WriteByte('\n')
	}
	return nil
}

func splitEvaluatedKnownHosts(value string) ([]string, error) {
	paths := strings.Fields(value)
	if len(paths) == 0 {
		return nil, fmt.Errorf("empty known-hosts path list: %w", ErrUnsupportedRoute)
	}
	for _, path := range paths {
		if strings.EqualFold(path, "none") {
			if len(paths) != 1 {
				return nil, fmt.Errorf("mixed none known-hosts path list: %w", ErrUnsupportedRoute)
			}
			continue
		}
		windowsProgramData := runtime.GOOS == "windows" && strings.HasPrefix(
			strings.ToUpper(strings.ReplaceAll(path, `\`, "/")), "__PROGRAMDATA__/",
		)
		if !filepath.IsAbs(path) && !windowsProgramData || strings.ContainsAny(path, "\r\n\x00") {
			return nil, fmt.Errorf("relative or whitespace-containing known-hosts paths cannot be copied safely: %w", ErrUnsupportedRoute)
		}
	}
	return paths, nil
}

func writeSelectedAuthentication(body *strings.Builder, effective EffectiveConfig, selector keySelector) error {
	if !validUTF8NoControl(selector.identity) || !filepath.IsAbs(selector.identity) ||
		selector.certificate != "" && (!validUTF8NoControl(selector.certificate) || !filepath.IsAbs(selector.certificate)) {
		return fmt.Errorf("selected identity path is unsafe: %w", ErrUnsafePath)
	}
	writeConfigDirective(body, "IdentityFile", "none")
	writeConfigDirective(body, "CertificateFile", "none")
	writeConfigDirective(body, "IdentityFile", selector.identity)
	if selector.certificate != "" {
		writeConfigDirective(body, "CertificateFile", selector.certificate)
	}
	writeConfigDirective(body, "IdentitiesOnly", "yes")
	writeConfigDirective(body, "PubkeyAuthentication", "yes")
	writeConfigDirective(body, "PreferredAuthentications", "publickey")
	writeConfigDirective(body, "PasswordAuthentication", "no")
	writeConfigDirective(body, "KbdInteractiveAuthentication", "no")
	writeConfigDirective(body, "ChallengeResponseAuthentication", "no")
	writeConfigDirective(body, "GSSAPIAuthentication", "no")
	writeConfigDirective(body, "HostbasedAuthentication", "no")
	writeConfigDirective(body, "NumberOfPasswordPrompts", "0")
	for _, option := range []struct {
		key       string
		directive string
	}{
		{key: "identityagent", directive: "IdentityAgent"},
		{key: "securitykeyprovider", directive: "SecurityKeyProvider"},
		{key: "pkcs11provider", directive: "PKCS11Provider"},
		{key: "pubkeyacceptedalgorithms", directive: "PubkeyAcceptedAlgorithms"},
		{key: "pubkeyacceptedkeytypes", directive: "PubkeyAcceptedKeyTypes"},
	} {
		if value := firstEffectiveValue(effective, option.key); value != "" {
			connectionPath := option.key == "identityagent" || option.key == "securitykeyprovider" || option.key == "pkcs11provider"
			if !validUTF8NoControl(value) || connectionPath && strings.Contains(value, "%") {
				return ErrUnsupportedRoute
			}
			writeConfigDirective(body, option.directive, value)
		}
	}
	return nil
}

func writeOrdinaryProxyAuthentication(body *strings.Builder, effective EffectiveConfig) error {
	writeConfigDirective(body, "IdentityFile", "none")
	for _, identity := range effective.IdentityFiles {
		if !validUTF8NoControl(identity) {
			return ErrUnsupportedRoute
		}
		writeConfigDirective(body, "IdentityFile", identity)
	}
	writeConfigDirective(body, "CertificateFile", "none")
	for _, certificate := range effective.Values["certificatefile"] {
		if !validUTF8NoControl(certificate) {
			return ErrUnsupportedRoute
		}
		writeConfigDirective(body, "CertificateFile", certificate)
	}
	for _, option := range []struct {
		key       string
		directive string
	}{
		{key: "identitiesonly", directive: "IdentitiesOnly"},
		{key: "identityagent", directive: "IdentityAgent"},
		{key: "securitykeyprovider", directive: "SecurityKeyProvider"},
		{key: "pkcs11provider", directive: "PKCS11Provider"},
		{key: "pubkeyauthentication", directive: "PubkeyAuthentication"},
		{key: "passwordauthentication", directive: "PasswordAuthentication"},
		{key: "kbdinteractiveauthentication", directive: "KbdInteractiveAuthentication"},
		{key: "challengeresponseauthentication", directive: "ChallengeResponseAuthentication"},
		{key: "gssapiauthentication", directive: "GSSAPIAuthentication"},
		{key: "hostbasedauthentication", directive: "HostbasedAuthentication"},
		{key: "preferredauthentications", directive: "PreferredAuthentications"},
		{key: "numberofpasswordprompts", directive: "NumberOfPasswordPrompts"},
		{key: "pubkeyacceptedalgorithms", directive: "PubkeyAcceptedAlgorithms"},
		{key: "pubkeyacceptedkeytypes", directive: "PubkeyAcceptedKeyTypes"},
		{key: "hostbasedacceptedalgorithms", directive: "HostbasedAcceptedAlgorithms"},
	} {
		if value := firstEffectiveValue(effective, option.key); value != "" {
			if !validUTF8NoControl(value) {
				return ErrUnsupportedRoute
			}
			writeConfigDirective(body, option.directive, value)
		}
	}
	return nil
}

func (s *Service) detectWindowsAdministrator(ctx context.Context, hop routeHopState, interactive bool) (bool, error) {
	result, err := s.runHopSSHWithCapture(
		ctx, hop, !interactive, windowsAdminProbeProgram(), nil, interactive,
		"ssh Windows administrator probe", true,
	)
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		if !interactive {
			return false, ErrInteractionRequired
		}
		return false, ErrManualRemediation
	}
	switch strings.TrimSpace(string(result.Stdout)) {
	case "administrator":
		return true, nil
	case "standard":
		return false, nil
	default:
		return false, ErrManualRemediation
	}
}

func (s *Service) runHopSSH(
	ctx context.Context,
	hop routeHopState,
	batch bool,
	program string,
	stdin []byte,
	interactive bool,
	display string,
) (RunResult, error) {
	return s.runHopSSHWithCapture(ctx, hop, batch, program, stdin, interactive, display, false)
}

func (s *Service) runHopSSHWithCapture(
	ctx context.Context,
	hop routeHopState,
	batch bool,
	program string,
	stdin []byte,
	interactive bool,
	display string,
	captureStdout bool,
) (RunResult, error) {
	args := appendFreshSSHOptions(nil, batch)
	if hop.proxyJump != "" {
		args = append(args, "-J", hop.proxyJump)
	}
	if hop.explicitUser != "" {
		args = append(args, "-l", hop.explicitUser)
	}
	if hop.explicitPort != 0 {
		args = append(args, "-p", strconv.Itoa(hop.explicitPort))
	}
	args = append(args, hop.destination, program)
	return s.runner.Run(ctx, RunRequest{
		Name: "ssh", Args: args, Stdin: stdin, Interactive: interactive,
		CaptureStdout: captureStdout, Display: display,
	})
}

func finalizeBootstrap(result BootstrapResult) BootstrapResult {
	allReady := len(result.Hops) > 0
	targetReady := false
	for _, hop := range result.Hops {
		allReady = allReady && hop.Ready
		if hop.Target {
			targetReady = hop.Ready && hop.OrdinaryReady
		}
	}
	result.Ready = allReady && targetReady
	result.FleetReady = result.Ready && result.TargetRemoteOS != RemoteOSUnknown
	result.Partial = !result.Ready
	if result.Ready {
		result.Status = BootstrapReady
		result.Code = "ready"
	} else {
		result.Status = BootstrapPartial
		result.Code = "partial"
	}
	return result
}
