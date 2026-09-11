package sshhost

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var diagnosticEndpoint = regexp.MustCompile(`(?m)^Connecting to [^\r\n]* \[([^\]\r\n]+)\] port ([0-9]+)\.$`)

func (s *Service) diagnosticSSHAttempt(ctx context.Context, target string, none bool) DiagnosticAttempt {
	started := time.Now()
	attemptCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := []string{"-vvv", "-n", "-T", "-S", "none", "-o", "BatchMode=yes", "-o", "ConnectionAttempts=1", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "ControlMaster=no", "-o", "ControlPersist=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "RemoteCommand=none", "-o", "RequestTTY=no", "-o", "SessionType=default", "-o", "ForkAfterAuthentication=no"}
	name := "baseline"
	if none {
		args = append(args, "-o", "IPQoS=none")
		name = "ipqos_none"
	}
	args = append(args, target, "exit 0")
	run, err := s.runner.Run(attemptCtx, RunRequest{Name: "ssh", Args: args, Env: []string{"LC_ALL=C"}, Display: "SSH diagnostic fresh login"})
	attempt := classifyDiagnosticSSH(name, run, err)
	attempt.ElapsedMS = time.Since(started).Milliseconds()
	return attempt
}
func classifyDiagnosticSSH(name string, run RunResult, runErr error) DiagnosticAttempt {
	a := DiagnosticAttempt{Name: name, Code: "ssh_failed", Truncated: run.StdoutTruncated || run.StderrTruncated}
	for _, stage := range []string{"transport", "handshake", "host_key", "authentication", "session"} {
		a.Stages = append(a.Stages, DiagnosticStage{Name: stage, State: "unknown", Code: "not_observed"})
	}
	stderr, remoteOutput := diagnosticClientPrefix(string(run.Stderr))
	networkLog := diagnosticNetworkPrefix(stderr)
	connected := diagnosticHasLine(networkLog, "Connection established.", true)
	if matches := diagnosticEndpoint.FindAllStringSubmatch(networkLog, -1); len(matches) > 0 {
		final := matches[len(matches)-1]
		a.Endpoint = diagnosticText(final[1])
		a.Port, _ = strconv.Atoi(final[2])
	}
	lower := strings.ToLower(stderr)
	for _, line := range strings.Split(strings.ToLower(networkLog), "\n") {
		if strings.HasPrefix(line, "set_sock_tos:") && (strings.Contains(line, "ip_tos") || strings.Contains(line, "ipv6_tclass")) {
			a.QoSMarked = true
		}
	}
	a.QoSMarked = a.QoSMarked && !strings.Contains(strings.ToLower(networkLog), "setsockopt")
	mark := func(name, state, code string) {
		for i := range a.Stages {
			if a.Stages[i].Name == name {
				a.Stages[i].State = state
				a.Stages[i].Code = code
			}
		}
	}
	if connected {
		mark("transport", "passed", "tcp_connected")
	}
	if diagnosticHasLine(stderr, "SSH2_MSG_NEWKEYS received", true) {
		mark("transport", "passed", "tcp_connected")
		mark("handshake", "passed", "handshake_complete")
	}
	if diagnosticHasHostProof(stderr) {
		mark("host_key", "passed", "host_key_verified")
	}
	authenticated := diagnosticHasLine(stderr, "Authenticated to ", false)
	if authenticated {
		for _, stage := range []string{"transport", "handshake", "host_key", "authentication"} {
			mark(stage, "passed", map[string]string{"transport": "tcp_connected", "handshake": "handshake_complete", "host_key": "host_key_verified", "authentication": "authenticated"}[stage])
		}
	}
	if runErr == nil && run.ExitCode == 0 {
		a.Ready = true
		a.Code = "ready"
		for i := range a.Stages {
			a.Stages[i].State = "passed"
			a.Stages[i].Code = []string{"tcp_connected", "handshake_complete", "host_key_verified", "authenticated", "remote_exit_zero"}[i]
		}
		return a
	}
	switch {
	case errors.Is(runErr, context.Canceled):
		a.Code = "canceled"
	case authenticated:
		a.Code = "session_failed"
		mark("session", "failed", a.Code)
	case remoteOutput:
		a.Code = "remote_output_ambiguous"
	case strings.Contains(stderr, "REMOTE HOST IDENTIFICATION HAS CHANGED"):
		a.Code = "host_key_changed"
		mark("host_key", "failed", a.Code)
	case strings.Contains(lower, "no ") && strings.Contains(lower, "host key is known for"):
		a.Code = "host_key_unknown"
		mark("host_key", "failed", a.Code)
	case strings.Contains(stderr, "Host key verification failed"):
		a.Code = "host_key_rejected"
		mark("host_key", "failed", a.Code)
	case strings.Contains(lower, "agent refused operation"):
		a.Code = "agent_refused"
		mark("authentication", "failed", a.Code)
	case strings.Contains(stderr, "Permission denied"):
		a.Code = "authentication_denied"
		mark("authentication", "failed", a.Code)
	case strings.Contains(lower, "connection refused"):
		a.Code = "connection_refused"
		mark("transport", "failed", a.Code)
	case errors.Is(runErr, context.DeadlineExceeded) || strings.Contains(lower, "timed out"):
		stage := "transport"
		a.Code = "connect_timeout"
		if attemptPassed(a, "handshake") {
			stage = "authentication"
			a.Code = "authentication_timeout"
		} else if attemptPassed(a, "transport") {
			stage = "handshake"
			a.Code = "handshake_timeout"
		}
		mark(stage, "failed", a.Code)
	case runErr != nil:
		a.Code = "ssh_unavailable"
	}
	// Host-key rejection occurs during key exchange and still proves progress
	// beyond the TCP stage, without asserting a completed handshake.
	if strings.HasPrefix(a.Code, "host_key_") {
		mark("transport", "passed", "tcp_connected")
		mark("handshake", "unknown", "host_identity_reached")
	}
	// A failed client command cannot establish completed authentication from
	// human log text. Preserve phase hints without converting them into success.
	for i := range a.Stages {
		stage := &a.Stages[i]
		if stage.State != "passed" {
			continue
		}
		if stage.Name == "transport" && connected {
			continue
		}
		stage.State = "unknown"
		stage.Code = stage.Name + "_reported"
	}
	if a.Truncated && !a.Ready {
		a.Code = "evidence_truncated"
		a.QoSMarked = false
		for i := range a.Stages {
			a.Stages[i].State = "unknown"
			a.Stages[i].Code = "evidence_truncated"
		}
	}
	return a
}
func attemptPassed(a DiagnosticAttempt, name string) bool {
	for _, s := range a.Stages {
		if s.Name == name {
			return s.State == "passed"
		}
	}
	return false
}

// Server banners/debug/disconnect text may contain arbitrary log-looking lines.
// Only the client-generated prefix before such text can supply partial proofs;
// exit zero still proves a completed fresh login independently of log wording.
func diagnosticClientPrefix(raw string) (string, bool) {
	var trusted []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		for _, prefix := range []string{"debug1: ", "debug2: ", "debug3: "} {
			if strings.HasPrefix(line, prefix) {
				line = strings.TrimPrefix(line, prefix)
				break
			}
		}
		if strings.HasPrefix(line, "receive packet: type 53") || strings.HasPrefix(line, "input_userauth_banner:") || strings.HasPrefix(line, "kex_exchange_identification: banner line ") || strings.HasPrefix(line, "Remote: ") || strings.HasPrefix(line, "Received disconnect from ") || strings.HasPrefix(line, "Unable to negotiate with ") {
			return strings.Join(trusted, "\n"), true
		}
		trusted = append(trusted, line)
	}
	return strings.Join(trusted, "\n"), false
}
func diagnosticHasLine(log, wanted string, exact bool) bool {
	for _, line := range strings.Split(log, "\n") {
		if exact && line == wanted || !exact && strings.HasPrefix(line, wanted) {
			return true
		}
	}
	return false
}
func diagnosticHasHostProof(log string) bool {
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "Host ") && strings.Contains(line, " is known and matches the ") || strings.HasPrefix(line, "Found CA key") {
			return true
		}
	}
	return false
}

func diagnosticNetworkPrefix(log string) string {
	var prefix []string
	for _, line := range strings.Split(log, "\n") {
		prefix = append(prefix, line)
		if line == "Connection established." {
			break
		}
	}
	return strings.Join(prefix, "\n")
}
