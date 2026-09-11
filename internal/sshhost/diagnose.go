package sshhost

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const DefaultDiagnoseTimeout = 60 * time.Second

// ValidateDiagnosticTarget deliberately does not relax managed/bootstrap aliases.
func ValidateDiagnosticTarget(target string) error {
	if err := ValidateLookupAlias(target); err != nil {
		return errors.New("invalid diagnostic SSH target")
	}
	if strings.ContainsAny(target, "/\\@") {
		return errors.New("use an SSH alias, hostname or IP literal as the diagnostic target")
	}
	if strings.Contains(target, ":") || strings.Contains(target, "%") {
		addr, err := netip.ParseAddr(target)
		if err != nil {
			return errors.New("invalid IP literal or IPv6 zone")
		}
		if zone := addr.Zone(); zone != "" {
			for _, c := range zone {
				if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '-' && c != '.' {
					return errors.New("invalid IPv6 zone")
				}
			}
		}
	}
	return nil
}

func newDiagnosis(target string) Diagnosis {
	d := Diagnosis{SchemaVersion: 1, Kind: "ssh_diagnosis", Privacy: "local", Status: "incomplete", Target: DiagnosticTarget{Name: target, Proxy: "none"}, Attempts: []DiagnosticAttempt{}, Findings: []string{}, SuggestedActions: []string{}}
	for _, name := range []string{"config", "dns", "route", "tcp", "banner", "qos"} {
		d.Stages = append(d.Stages, DiagnosticStage{Name: name, State: "skipped", Code: "prerequisite_unavailable"})
	}
	return d
}
func (d *Diagnosis) stage(name, state, code string, start time.Time) {
	for i := range d.Stages {
		if d.Stages[i].Name == name {
			d.Stages[i] = DiagnosticStage{Name: name, State: state, Code: code, ElapsedMS: time.Since(start).Milliseconds()}
			return
		}
	}
}
func diagnosticText(s string) string {
	if len(s) > 512 {
		return ""
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return ""
	}
	return s
}
func diagnosticOption(e EffectiveConfig, name string) string {
	value := firstEffectiveValue(e, name)
	if value == "none" {
		return ""
	}
	return diagnosticText(value)
}

// Diagnose is explicit network activity. It does not require a statically
// selectable alias, and it never changes SSH/network files or imports host keys.
func (s *Service) Diagnose(ctx context.Context, request DiagnoseRequest) (Diagnosis, error) {
	d := newDiagnosis(request.Target)
	if err := ValidateDiagnosticTarget(request.Target); err != nil {
		return d, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = DefaultDiagnoseTimeout
	}
	if timeout < 0 {
		return d, errors.New("diagnostic timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	configCtx, configCancel := context.WithTimeout(ctx, 5*time.Second)
	run, err := s.runner.Run(configCtx, RunRequest{Name: "ssh", Args: []string{"-G", request.Target}, Env: []string{"LC_ALL=C"}, Display: "SSH diagnostic effective configuration"})
	configCancel()
	if err != nil || run.ExitCode != 0 || run.StdoutTruncated {
		d.stage("config", "failed", "config_unavailable", start)
		d.SuggestedActions = append(d.SuggestedActions, "inspect_ssh_config")
		return finishDiagnosis(ctx, d, errors.New("SSH effective configuration unavailable"))
	}
	effective, err := ParseEffective(request.Target, run.Stdout)
	if err != nil || effective.HostName == "" || effective.Port == 0 || diagnosticText(effective.HostName) == "" {
		d.stage("config", "failed", "config_invalid", start)
		return d, errors.New("SSH effective configuration is invalid")
	}
	d.Target = DiagnosticTarget{Name: request.Target, Hostname: effective.HostName, Port: effective.Port, User: diagnosticText(effective.User), IPQoS: diagnosticText(firstEffectiveValue(effective, "ipqos")), Proxy: "none", AddressFamily: diagnosticText(firstEffectiveValue(effective, "addressfamily")), BindAddress: diagnosticOption(effective, "bindaddress"), BindInterface: diagnosticOption(effective, "bindinterface")}
	for _, identity := range effective.IdentityFiles {
		if value := diagnosticText(identity); value != "" && len(d.Target.IdentityFiles) < 32 {
			d.Target.IdentityFiles = append(d.Target.IdentityFiles, value)
		}
	}
	if diagnosticOption(effective, "proxyjump") != "" {
		d.Target.Proxy = "jump"
	}
	if diagnosticOption(effective, "proxycommand") != "" {
		d.Target.Proxy = "command"
	}
	d.stage("config", "passed", "config_ready", start)
	hooks := s.options.Diagnostics
	if hooks.LookupIP == nil {
		hooks.LookupIP = net.DefaultResolver.LookupIPAddr
	}
	if hooks.Route == nil {
		hooks.Route = s.diagnosticRoute
	}
	if hooks.Dial == nil {
		dialer := &net.Dialer{}
		if ip, parseErr := netip.ParseAddr(d.Target.BindAddress); parseErr == nil {
			dialer.LocalAddr = &net.TCPAddr{IP: net.IP(ip.AsSlice()), Zone: ip.Zone()}
		}
		hooks.Dial = dialer.DialContext
	}
	if d.Target.Proxy != "none" {
		for _, name := range []string{"dns", "route", "tcp", "banner"} {
			d.stage(name, "skipped", "proxy_path", time.Now())
		}
	} else {
		s.diagnoseNetwork(ctx, &d, hooks)
	}
	if ctx.Err() != nil {
		return finishDiagnosis(ctx, d, ctx.Err())
	}
	baseline := s.diagnosticSSHAttempt(ctx, request.Target, false)
	d.Attempts = append(d.Attempts, baseline)
	if baseline.Ready {
		d.Status = "ready"
	} else {
		d.Status = "not_ready"
		d.SuggestedActions = append(d.SuggestedActions, diagnosticNextAction(baseline.Code))
	}
	d.stage("qos", "skipped", "not_requested", time.Now())
	if request.CompareQoS {
		code := "qos_comparison_unavailable"
		classes := strings.Fields(d.Target.IPQoS)
		switch {
		case d.Target.Proxy != "none" || d.Target.BindInterface != "":
			code = "path_not_comparable"
		case len(classes) > 0 && classes[0] == "none":
			code = "qos_already_disabled"
		case baseline.Ready:
			code = "baseline_ready"
		case !baseline.QoSMarked:
			code = "qos_marking_unproven"
		case baseline.Code != "connect_timeout" && baseline.Code != "handshake_timeout":
			code = "no_transport_timeout"
		case baseline.Endpoint == "" || !s.diagnosticComparisonStable(ctx, effective, d, baseline, hooks):
			code = "path_not_comparable"
		default:
			if ctx.Err() != nil {
				return finishDiagnosis(ctx, d, ctx.Err())
			}
			compared := s.diagnosticSSHAttempt(ctx, request.Target, true)
			d.Attempts = append(d.Attempts, compared)
			code = "qos_no_progress"
			if compared.Endpoint != baseline.Endpoint || compared.Port != baseline.Port {
				code = "path_not_comparable"
			} else if compared.Ready || attemptPassed(compared, "handshake") || strings.HasPrefix(compared.Code, "host_key_") {
				code = "qos_correlated_progress"
				d.Findings = append(d.Findings, code)
				d.SuggestedActions = append(d.SuggestedActions, "review_per_host_ipqos")
			}
		}
		state := "unknown"
		if code == "qos_correlated_progress" || code == "qos_no_progress" {
			state = "passed"
		}
		if code == "qos_marking_unproven" {
			state = "unsupported"
		}
		d.stage("qos", state, code, time.Now())
	}
	if ctx.Err() != nil {
		return finishDiagnosis(ctx, d, ctx.Err())
	}
	if !baseline.Ready {
		return d, errors.New("SSH diagnostic baseline did not complete a verified login; see stage results")
	}
	return d, nil
}
func finishDiagnosis(ctx context.Context, d Diagnosis, err error) (Diagnosis, error) {
	if ctx.Err() != nil {
		for i := range d.Stages {
			if d.Stages[i].Code == "prerequisite_unavailable" {
				d.Stages[i].State = "canceled"
				d.Stages[i].Code = "canceled"
			}
		}
		d.Status = "incomplete"
		return d, ctx.Err()
	}
	return d, err
}
func (s *Service) diagnoseNetwork(ctx context.Context, d *Diagnosis, hooks DiagnosticHooks) {
	start := time.Now()
	dnsCtx, dnsCancel := context.WithTimeout(ctx, 5*time.Second)
	var ips []net.IPAddr
	if ip, err := netip.ParseAddr(d.Target.Hostname); err == nil {
		ips = []net.IPAddr{{IP: net.IP(ip.AsSlice()), Zone: ip.Zone()}}
	} else {
		ips, _ = hooks.LookupIP(dnsCtx, d.Target.Hostname)
	}
	dnsCancel()
	seen := map[string]bool{}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		if d.Target.AddressFamily == "inet" && !addr.Is4() || d.Target.AddressFamily == "inet6" && !addr.Is6() {
			continue
		}
		if ip.Zone != "" {
			addr = addr.WithZone(ip.Zone)
		}
		value := addr.String()
		if seen[value] {
			continue
		}
		seen[value] = true
		if len(d.Addresses) == 4 {
			d.AddressesLimited = true
			continue
		}
		d.Addresses = append(d.Addresses, value)
	}
	if len(d.Addresses) == 0 {
		d.stage("dns", "failed", "dns_unavailable", start)
		return
	}
	d.stage("dns", "passed", "addresses_resolved", start)
	start = time.Now()
	routeCtx, routeCancel := context.WithTimeout(ctx, 10*time.Second)
	routeOK := true
	for _, addr := range d.Addresses {
		observation, err := hooks.Route(routeCtx, DiagnosticRouteQuery{Address: addr, Port: d.Target.Port, Source: d.Target.BindAddress, Interface: d.Target.BindInterface})
		if err != nil {
			routeOK = false
			if observation.Code == "" {
				observation.Code = "route_unavailable"
			}
		}
		d.Routes = append(d.Routes, observation)
	}
	routeCancel()
	if routeOK {
		d.stage("route", "passed", "route_observed", start)
	} else {
		d.stage("route", "unknown", "route_unavailable", start)
	}
	if d.Target.BindInterface != "" {
		d.stage("tcp", "unsupported", "bind_interface_unavailable", time.Now())
		return
	}
	if d.Target.BindAddress != "" {
		if _, err := netip.ParseAddr(d.Target.BindAddress); err != nil {
			d.stage("tcp", "unsupported", "bind_address_unavailable", time.Now())
			return
		}
	}
	start = time.Now()
	tcpCtx, tcpCancel := context.WithTimeout(ctx, 5*time.Second)
	defer tcpCancel()
	var conn net.Conn
	code := "tcp_failed"
	for _, addr := range d.Addresses {
		ip, _ := netip.ParseAddr(addr)
		if ip.IsLinkLocalUnicast() && ip.Zone() == "" {
			code = "scope_required"
			continue
		}
		var err error
		conn, err = hooks.Dial(tcpCtx, "tcp", net.JoinHostPort(addr, strconv.Itoa(d.Target.Port)))
		if err == nil {
			break
		}
		code = diagnosticNetworkError(err)
	}
	if conn == nil {
		d.stage("tcp", "failed", code, start)
		return
	}
	defer conn.Close()
	d.SocketLocal = diagnosticText(conn.LocalAddr().String())
	d.SocketRemote = diagnosticText(conn.RemoteAddr().String())
	d.stage("tcp", "passed", "tcp_connected", start)
	start = time.Now()
	deadline := time.Now().Add(3 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = conn.SetReadDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	scanner := bufio.NewScanner(io.LimitReader(conn, 8192))
	scanner.Buffer(make([]byte, 256), 1024)
	for count := 0; count < 20 && scanner.Scan(); count++ {
		line := scanner.Text()
		if strings.HasPrefix(line, "SSH-2.0-") || strings.HasPrefix(line, "SSH-1.99-") {
			d.stage("banner", "passed", "ssh_banner", start)
			return
		}
	}
	code = "non_ssh_banner"
	if err := scanner.Err(); err != nil {
		code = "banner_failed"
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			code = "banner_timeout"
		}
	}
	d.stage("banner", "failed", code, start)
}
func diagnosticNetworkError(err error) string {
	var timed net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timed) && timed.Timeout() {
		return "connect_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	// OS errno text is consumed only for classification, never published.
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "refused") {
		return "connection_refused"
	}
	return "tcp_failed"
}
func diagnosticNextAction(code string) string {
	switch code {
	case "host_key_unknown", "host_key_changed", "host_key_rejected":
		return "verify_host_identity"
	case "authentication_denied", "agent_refused":
		return "check_authentication"
	case "session_failed":
		return "check_remote_session"
	default:
		return "inspect_connection_path"
	}
}

// Re-evaluate effective settings and the observed route before a controlled
// comparison. Changes or unavailable proof make the result non-comparable.
func (s *Service) diagnosticComparisonStable(ctx context.Context, original EffectiveConfig, d Diagnosis, baseline DiagnosticAttempt, hooks DiagnosticHooks) bool {
	var prior *DiagnosticRoute
	for i := range d.Routes {
		if d.Routes[i].Address == baseline.Endpoint && d.Routes[i].Code == "route_observed" {
			prior = &d.Routes[i]
			break
		}
	}
	if prior == nil {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	run, err := s.runner.Run(checkCtx, RunRequest{Name: "ssh", Args: []string{"-G", d.Target.Name}, Env: []string{"LC_ALL=C"}, Display: "SSH diagnostic comparison configuration"})
	if err != nil || run.ExitCode != 0 || run.StdoutTruncated {
		return false
	}
	current, err := ParseEffective(d.Target.Name, run.Stdout)
	if err != nil || !reflect.DeepEqual(original.Values, current.Values) {
		return false
	}
	route, err := hooks.Route(checkCtx, DiagnosticRouteQuery{Address: baseline.Endpoint, Port: baseline.Port, Source: d.Target.BindAddress, Interface: d.Target.BindInterface})
	return err == nil && reflect.DeepEqual(*prior, route)
}
