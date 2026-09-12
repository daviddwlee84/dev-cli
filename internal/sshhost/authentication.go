package sshhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
)

type AuthenticationProbe struct {
	HopIndex               int    `json:"hop_index"`
	Alias                  string `json:"alias"`
	Ready                  bool   `json:"ready"`
	HostTrusted            bool   `json:"host_trusted"`
	AuthenticationRequired bool   `json:"authentication_required"`
	PasswordAvailable      bool   `json:"password_available"`
	Method                 string `json:"method,omitempty"`
	Code                   string `json:"code"`
	ExitCode               int    `json:"exit_code"`
}

// PasswordProof is minted only from an acknowledged single broker delivery and
// a fresh native password authentication event. It contains no secret.
type PasswordProof struct {
	HopIndex  int    `json:"hop_index"`
	Alias     string `json:"alias"`
	Verified  bool   `json:"verified"`
	operation *AuthenticationOperation
}

// AuthenticationOperation owns controller-local, operation-only passwords and
// native per-hop connectors. It never writes credentials or ordinary SSH config.
type AuthenticationOperation struct {
	Route      Route `json:"route"`
	mu         sync.Mutex
	runMu      sync.Mutex
	service    *Service
	route      Route
	ctx        context.Context
	cancel     context.CancelFunc
	server     *connectorServer
	executable string
	nativeEnv  []string
	passwords  map[int]*sshcredential.Broker
	trusted    map[int]bool
	closed     bool
	used       bool
	closedDone chan struct{}
}

func (s *Service) PrepareAuthentication(ctx context.Context, alias string) (*AuthenticationOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	route, err := s.ResolveRoute(ctx, RouteRequest{Alias: alias})
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	opCtx, cancel := context.WithCancel(ctx)
	server, err := newConnectorServer(opCtx)
	if err != nil {
		cancel()
		return nil, err
	}
	nativeEnv := []string{"SSH_AUTH_SOCK=" + os.Getenv("SSH_AUTH_SOCK"), "SSH_ASKPASS=" + os.Getenv("SSH_ASKPASS"), "SSH_ASKPASS_REQUIRE=" + os.Getenv("SSH_ASKPASS_REQUIRE")}
	return &AuthenticationOperation{Route: cloneRoute(route), service: s, route: route, ctx: opCtx, cancel: cancel, server: server, executable: executable, nativeEnv: nativeEnv, passwords: map[int]*sshcredential.Broker{}, trusted: map[int]bool{}, closedDone: make(chan struct{})}, nil
}
func (op *AuthenticationOperation) Close() error {
	if op == nil || op.service == nil {
		return nil
	}
	if op.cancel != nil {
		op.cancel()
	}
	op.mu.Lock()
	if op.closed {
		op.mu.Unlock()
		<-op.closedDone
		return nil
	}
	op.closed = true
	brokers := op.passwords
	op.passwords = map[int]*sshcredential.Broker{}
	op.mu.Unlock()
	if op.server != nil {
		op.server.close()
	}
	for _, broker := range brokers {
		_ = broker.Close()
	}
	close(op.closedDone)
	return nil
}

// UsesPassword reports only whether this live operation retains any per-hop
// password context; it never exposes or resolves credential material.
func (op *AuthenticationOperation) UsesPassword() bool {
	if op == nil {
		return false
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	return !op.closed && len(op.passwords) > 0
}
func (op *AuthenticationOperation) check(ctx context.Context, index int) error {
	if op == nil || op.service == nil {
		return errors.New("SSH authentication operation is unavailable")
	}
	op.mu.Lock()
	closed := op.closed
	op.mu.Unlock()
	if closed {
		return errors.New("SSH authentication operation is closed")
	}
	if err := op.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if index < 0 || index >= len(op.route.Hops) || !routesEqual(op.Route, op.route) {
		return ErrSourceChanged
	}
	fresh, err := op.service.ResolveRoute(ctx, op.route.state.request)
	if err != nil {
		return err
	}
	if !sameConnectionRoute(op.route, fresh) {
		return ErrSourceChanged
	}
	return nil
}

func (op *AuthenticationOperation) ProbeHop(ctx context.Context, index int) (AuthenticationProbe, error) {
	if op == nil || op.service == nil {
		return AuthenticationProbe{}, ErrSourceChanged
	}
	op.runMu.Lock()
	defer op.runMu.Unlock()
	return op.probeHop(ctx, index, false)
}
func (op *AuthenticationOperation) TrustHop(ctx context.Context, index int) (AuthenticationProbe, error) {
	if op == nil || op.service == nil {
		return AuthenticationProbe{}, ErrSourceChanged
	}
	op.runMu.Lock()
	defer op.runMu.Unlock()
	return op.probeHop(ctx, index, true)
}
func (op *AuthenticationOperation) probeHop(ctx context.Context, index int, trust bool) (AuthenticationProbe, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := op.check(ctx, index); err != nil {
		return AuthenticationProbe{}, err
	}
	mode := "ordinary"
	if trust {
		mode = "trust"
	}
	run, log, err := op.runPhase(ctx, index, mode, keySelector{}, ConnectionOptions{Args: []string{"exit 0"}, Interactive: trust, SuppressForwarding: true}, true, nil)
	probe := AuthenticationProbe{HopIndex: index, Alias: op.route.Hops[index].Alias, ExitCode: run.ExitCode, Code: "not_ready"}
	if err != nil {
		return probe, err
	}
	events, method, keyless := authenticationEvents(log)
	probe.Method = method
	probe.Ready = run.ExitCode == 0 && events == 1
	probe.HostTrusted = probe.Ready
	for _, line := range strings.Split(string(log), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSuffix(line, ".") == "debug1: SSH2_MSG_SERVICE_ACCEPT received" {
			probe.HostTrusted = true
		}
		if methods, ok := strings.CutPrefix(line, "debug1: Authentications that can continue: "); ok {
			for _, method := range strings.Split(strings.TrimSpace(methods), ",") {
				if method == "password" {
					probe.PasswordAvailable = true
				}
			}
		}
	}
	probe.AuthenticationRequired = !probe.Ready && probe.HostTrusted && run.ExitCode == 255
	if keyless && probe.Ready {
		probe.Method = "none"
	}
	if probe.Ready {
		probe.Code = "ready"
	} else if probe.AuthenticationRequired {
		probe.Code = "authentication_required"
	} else if trust {
		probe.Code = "host_trust_not_verified"
	}
	if probe.HostTrusted {
		op.mu.Lock()
		op.trusted[index] = true
		op.mu.Unlock()
	}
	return probe, nil
}

func (op *AuthenticationOperation) ProvePassword(ctx context.Context, index int, secret []byte) (PasswordProof, error) {
	if op == nil || op.service == nil {
		return PasswordProof{}, ErrSourceChanged
	}
	op.runMu.Lock()
	defer op.runMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := op.check(ctx, index); err != nil {
		return PasswordProof{}, err
	}
	op.mu.Lock()
	trusted := op.trusted[index]
	op.mu.Unlock()
	if !trusted {
		return PasswordProof{}, errors.New("complete the native host-key trust gate before a password proof")
	}
	subject, err := op.passwordContext(index)
	if err != nil {
		return PasswordProof{}, err
	}
	broker, err := sshcredential.NewBroker(ctx, []sshcredential.PasswordAnswer{{Context: subject, Secret: secret}})
	if err != nil {
		return PasswordProof{}, err
	}
	run, log, runErr := op.runPhase(ctx, index, "password", keySelector{}, ConnectionOptions{Args: []string{"exit 0"}, SuppressForwarding: true}, true, broker)
	closeErr := broker.Close()
	served, denied := broker.Usage(subject.ID)
	proof := PasswordProof{HopIndex: index, Alias: op.route.Hops[index].Alias}
	if runErr != nil {
		return proof, runErr
	}
	if closeErr != nil {
		return proof, closeErr
	}
	events, method, keyless := authenticationEvents(log)
	if run.ExitCode != 0 || events != 1 || method != "password" || keyless || served != 1 || denied != 0 {
		return proof, ErrUnprovenAuthentication
	}
	retained, err := sshcredential.NewBroker(op.ctx, []sshcredential.PasswordAnswer{{Context: subject, Secret: secret}})
	if err != nil {
		return proof, err
	}
	op.mu.Lock()
	if op.closed {
		op.mu.Unlock()
		_ = retained.Close()
		return proof, context.Canceled
	}
	previous := op.passwords[index]
	op.passwords[index] = retained
	op.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	proof.Verified, proof.operation = true, op
	return proof, nil
}

func (op *AuthenticationOperation) Run(ctx context.Context, options ConnectionOptions) (RunResult, error) {
	return op.runConnection(ctx, nil, options)
}

// RunSelected keeps the target bound to one selected key while prior hops use
// their independently proven operation credentials.
func (op *AuthenticationOperation) RunSelected(ctx context.Context, candidate *KeyCandidate, options ConnectionOptions) (RunResult, error) {
	if candidate == nil {
		return RunResult{}, errors.New("selected connection requires a key candidate")
	}
	return op.runConnection(ctx, candidate, options)
}

func (op *AuthenticationOperation) runConnection(ctx context.Context, candidate *KeyCandidate, options ConnectionOptions) (RunResult, error) {
	if op == nil || op.service == nil {
		return RunResult{}, ErrSourceChanged
	}
	op.runMu.Lock()
	defer op.runMu.Unlock()
	defer op.Close()
	if ctx == nil {
		ctx = context.Background()
	}
	index := len(op.route.Hops) - 1
	if err := op.check(ctx, index); err != nil {
		return RunResult{}, err
	}
	if err := op.ValidateSession(options); err != nil {
		return RunResult{}, err
	}
	op.mu.Lock()
	used := op.used
	op.used = true
	op.mu.Unlock()
	if used {
		return RunResult{}, errors.New("SSH authentication operation already executed its command")
	}
	mode := "ordinary"
	var selector keySelector
	if candidate != nil {
		material, err := op.service.validateKeyCandidate(*candidate)
		if err != nil {
			return RunResult{}, err
		}
		verification, err := op.service.verifyKeyPair(ctx, material, options.Interactive)
		if err != nil {
			return RunResult{}, err
		}
		copy := *material
		copy.pairVerification = cloneKeyPairVerification(verification)
		var cleanup func()
		selector, cleanup, err = op.service.prepareKeySelector(&copy)
		if err != nil {
			return RunResult{}, err
		}
		defer cleanup()
		if selector.agent != nil {
			configured, enabled, err := op.service.resolveIdentityAgent(firstEffectiveValue(op.route.state.hops[index].effective, "identityagent"))
			if err != nil || !enabled || configured != "" && configured != selector.agent.socket {
				return RunResult{}, ErrAgentPolicyMismatch
			}
		}
		mode = "selected"
	}
	run, _, err := op.runPhase(ctx, index, mode, selector, options, false, nil)
	return run, err
}

func (op *AuthenticationOperation) passwordContext(index int) (sshcredential.PasswordContext, error) {
	hop := op.route.Hops[index]
	if hop.User == "" || hop.Port == 0 {
		return sshcredential.PasswordContext{}, ErrUnsupportedRoute
	}
	names := []string{hop.HostName}
	if alias := firstEffectiveValue(op.route.state.hops[index].effective, "hostkeyalias"); alias != "" {
		names = []string{alias}
	}
	return sshcredential.PasswordContext{ID: "hop-" + strconv.Itoa(index), User: hop.User, HostNames: names, Port: hop.Port}, nil
}

func authenticationEvents(log []byte) (int, string, bool) {
	events, method, keyless := 0, "", false
	for _, raw := range strings.Split(string(log), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if strings.Contains(line, "remote software version Tailscale") {
			keyless = true
		}
		match := modernAuthentication.FindStringSubmatch(line)
		if match == nil {
			match = legacyAuthentication.FindStringSubmatch(line)
		}
		if match != nil {
			events++
			method = match[1]
		}
	}
	return events, method, keyless
}

func (op *AuthenticationOperation) runPhase(ctx context.Context, index int, mode string, selector keySelector, options ConnectionOptions, proof bool, override *sshcredential.Broker) (RunResult, []byte, error) {
	phaseCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(op.ctx, cancel)
	defer stop()
	id, err := newConnectorID()
	if err != nil {
		return RunResult{}, nil, err
	}
	var staged []*stagedFile
	defer func() {
		for i := len(staged) - 1; i >= 0; i-- {
			_ = staged[i].discard()
		}
	}()
	specs := make([]connectorSpec, index)
	var targetPath, targetName string
	var targetEnv []string
	for hopIndex := 0; hopIndex <= index; hopIndex++ {
		hop := op.route.state.hops[hopIndex]
		op.mu.Lock()
		broker := op.passwords[hopIndex]
		op.mu.Unlock()
		hopMode := "ordinary"
		if broker != nil {
			hopMode = "password"
		}
		if hopIndex == index {
			if mode != "ordinary" {
				hopMode = mode
			}
			if override != nil {
				broker = override
			}
			if mode == "trust" || mode == "selected" {
				broker = nil
			}
		}
		proxy := ""
		if hopIndex > 0 {
			proxy, err = connectorCommand(op.executable, id, hopIndex-1)
			if err != nil {
				return RunResult{}, nil, err
			}
		}
		name := "dev-cli-auth-hop-" + strconv.Itoa(hopIndex)
		content, err := renderAuthenticationHop(hop, name, proxy, hopMode, selector, options.Interactive)
		if err != nil {
			return RunResult{}, nil, err
		}
		if !proof && hopIndex == index {
			content, err = appendSessionPolicy(content, hop.effective, options)
			if err != nil {
				return RunResult{}, nil, err
			}
		}
		config, err := createStagedFile(op.service.paths.SSHDir, content, nil)
		if err != nil {
			return RunResult{}, nil, err
		}
		staged = append(staged, config)
		path := filepath.Join(config.dir, config.name)
		env := []string{"LC_ALL=C", connectorEndpointEnv + "=" + op.server.endpoint, "DEV_SSH_ASKPASS_BROKER=", "DEV_FLEET_SSH_ASKPASS="}
		env = append(env, op.nativeEnv...)
		if broker != nil && hopMode == "password" {
			context, err := op.passwordContext(hopIndex)
			if err != nil {
				return RunResult{}, nil, err
			}
			secretEnv, err := broker.Env(op.executable, context.ID)
			if err != nil {
				return RunResult{}, nil, err
			}
			env = append(env, secretEnv...)
		}
		if hopIndex == index {
			targetPath, targetName, targetEnv = path, name, env
			continue
		}
		hash, err := connectorSnapshot(path)
		if err != nil {
			return RunResult{}, nil, err
		}
		next := op.route.Hops[hopIndex+1]
		specs[hopIndex] = connectorSpec{Config: path, ConfigHash: hash, Destination: name, Forward: net.JoinHostPort(next.HostName, strconv.Itoa(next.Port)), Env: env}
	}
	unpublish, err := op.server.publish(id, specs)
	if err != nil {
		return RunResult{}, nil, err
	}
	defer unpublish()
	args := appendFreshSSHOptions([]string{"-F", targetPath}, false)
	var log *stagedFile
	if proof {
		log, err = createStagedFile(op.service.paths.SSHDir, nil, nil)
		if err != nil {
			return RunResult{}, nil, err
		}
		staged = append(staged, log)
		args = append(args, "-E", filepath.Join(log.dir, log.name), "-o", "LogLevel=DEBUG1")
	}
	if options.SuppressForwarding {
		args = append(args, "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "PermitLocalCommand=no")
	}
	if options.ForwardAgentNo && !options.SuppressForwarding {
		args = append(args, "-o", "ForwardAgent=no")
	}
	args = append(args, targetName)
	args = append(args, options.Args...)
	run, runErr := op.service.runner.Run(phaseCtx, RunRequest{Name: "ssh", Args: args, Env: targetEnv, Stdin: options.Stdin, Interactive: options.Interactive, CaptureStdout: options.CaptureStdout, Display: "SSH per-hop authentication operation"})
	if !proof {
		return run, nil, runErr
	}
	snapshot, readErr := readSecureFileAt(log.root, log.name, filepath.Join(log.dir, log.name), false)
	if readErr != nil || !snapshot.exists || !os.SameFile(log.snapshot.info, snapshot.info) {
		return run, nil, ErrUnprovenAuthentication
	}
	return run, snapshot.data, runErr
}

func renderAuthenticationHop(hop routeHopState, name, proxy, mode string, selector keySelector, interactive bool) ([]byte, error) {
	if !validRouteHop(hop.safe) {
		return nil, ErrUnsupportedRoute
	}
	var body strings.Builder
	body.WriteString("# private dev-cli per-hop authentication configuration\nHost " + name + "\n")
	writeConfigDirective(&body, "HostName", hop.safe.HostName)
	writeConfigDirective(&body, "User", hop.safe.User)
	writeConfigDirective(&body, "Port", strconv.Itoa(hop.safe.Port))
	writeConfigDirective(&body, "ControlMaster", "no")
	writeConfigDirective(&body, "ControlPath", "none")
	writeConfigDirective(&body, "ControlPersist", "no")
	batch := "yes"
	if interactive || mode == "password" {
		batch = "no"
	}
	writeConfigDirective(&body, "BatchMode", batch)
	writeConfigDirective(&body, "ConnectTimeout", sshConnectTimeout)
	writeConfigDirective(&body, "ServerAliveInterval", sshServerAliveInterval)
	writeConfigDirective(&body, "ServerAliveCountMax", sshServerAliveCountMax)
	if err := writeExactHostKeyPolicy(&body, hop.effective, true); err != nil {
		return nil, err
	}
	for _, key := range []string{"bindaddress", "bindinterface", "compression", "tcpkeepalive"} {
		if value := firstEffectiveValue(hop.effective, key); value != "" {
			if !validUTF8NoControl(value) {
				return nil, ErrUnsupportedRoute
			}
			writeConfigDirective(&body, key, value)
		}
	}
	if value := firstEffectiveValue(hop.effective, "ipqos"); value != "" {
		fields := strings.Fields(value)
		if len(fields) < 1 || len(fields) > 2 {
			return nil, ErrUnsupportedRoute
		}
		body.WriteString("    IPQoS")
		for _, field := range fields {
			if !validUTF8NoControl(field) {
				return nil, ErrUnsupportedRoute
			}
			body.WriteByte(' ')
			body.WriteString(quoteConfigValue(field))
		}
		body.WriteByte('\n')
	}
	if proxy != "" {
		// ProxyCommand consumes the remainder verbatim; quoting the entire
		// command would turn executable plus arguments into one shell word.
		if !validUTF8NoControl(proxy) {
			return nil, ErrUnsupportedRoute
		}
		body.WriteString("    ProxyCommand " + proxy + "\n")
	}
	switch mode {
	case "selected":
		if err := writeSelectedAuthentication(&body, hop.effective, selector, interactive); err != nil {
			return nil, err
		}
	case "password", "trust":
		writeConfigDirective(&body, "IdentityFile", "none")
		writeConfigDirective(&body, "CertificateFile", "none")
		writeConfigDirective(&body, "PubkeyAuthentication", "no")
		writeConfigDirective(&body, "KbdInteractiveAuthentication", "no")
		writeConfigDirective(&body, "GSSAPIAuthentication", "no")
		writeConfigDirective(&body, "HostbasedAuthentication", "no")
		if mode == "password" {
			writeConfigDirective(&body, "PasswordAuthentication", "yes")
			writeConfigDirective(&body, "PreferredAuthentications", "password")
			writeConfigDirective(&body, "NumberOfPasswordPrompts", "1")
		} else {
			writeConfigDirective(&body, "PasswordAuthentication", "no")
			writeConfigDirective(&body, "PreferredAuthentications", "none")
			writeConfigDirective(&body, "NumberOfPasswordPrompts", "0")
		}
	case "ordinary":
		if err := writeOrdinaryProxyAuthentication(&body, hop.effective); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported SSH authentication mode")
	}
	return []byte(body.String()), nil
}
