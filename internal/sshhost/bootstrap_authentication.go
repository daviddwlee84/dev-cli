package sshhost

import (
	"context"
	"errors"
	"reflect"
	"strings"
)

type bootstrapAuthentication struct {
	operation *AuthenticationOperation
	index     int
	hardware  *keySelector
}

func sameAuthenticationRoute(left, right Route) bool {
	if left.Alias != right.Alias || left.state == nil || right.state == nil || len(left.Hops) != len(right.Hops) {
		return false
	}
	for index, hop := range left.Hops {
		other := right.Hops[index]
		if hop.Alias != other.Alias || hop.Reference != other.Reference || hop.HostName != other.HostName || hop.User != other.User || hop.Port != other.Port || !reflect.DeepEqual(left.state.hops[index].effective, right.state.hops[index].effective) {
			return false
		}
	}
	return true
}

func (s *Service) bootstrapProof(ctx context.Context, op *AuthenticationOperation, index int, hop routeHopState, selector keySelector, exact bool, nativeInteraction ...bool) (bool, error) {
	interactive := (exact || selector.ordinaryHardwareGate) && selector.securityKey && len(nativeInteraction) > 0 && nativeInteraction[0]
	if op == nil {
		return s.runSSHProof(ctx, hop, selector, exact, interactive)
	}
	if err := s.revalidateSecurityKeyState(selector.hardware); err != nil {
		return false, err
	}
	op.runMu.Lock()
	defer op.runMu.Unlock()
	if err := op.check(ctx, index); err != nil {
		return false, err
	}
	mode := "ordinary"
	if !exact && selector.ordinaryHardwareGate && interactive {
		mode = "hardware-ordinary"
	}
	if exact {
		mode = "selected"
		if selector.agent != nil {
			if !s.effectiveUsesAgent(hop.effective, selector.agent, selector.agent.identity != nil) {
				return false, errors.Join(ErrUnprovenAuthentication, ErrAgentPolicyMismatch)
			}
			if err := s.revalidateAgentKey(ctx, selector.agent, selector.fingerprint); err != nil {
				return false, err
			}
		}
	}
	run, log, err := op.runPhase(ctx, index, mode, selector, ConnectionOptions{Args: []string{"exit 0"}, SuppressForwarding: true, Interactive: interactive}, true, nil)
	if err != nil {
		return false, err
	}
	if exact || mode == "hardware-ordinary" {
		return selectedKeyAuthentication(log, run.ExitCode)
	}
	return run.ExitCode == 0, nil
}

func (s *Service) bootstrapRunHop(ctx context.Context, op *AuthenticationOperation, index int, hop routeHopState, batch bool, program string, stdin []byte, interactive bool, display string) (RunResult, error) {
	if op == nil {
		return s.runHopSSH(ctx, hop, batch, program, stdin, interactive, display)
	}
	op.runMu.Lock()
	defer op.runMu.Unlock()
	if err := op.check(ctx, index); err != nil {
		return RunResult{}, err
	}
	run, _, err := op.runPhase(ctx, index, "ordinary", keySelector{}, ConnectionOptions{Args: []string{program}, Stdin: stdin, Interactive: interactive, SuppressForwarding: true}, false, nil)
	return run, err
}

func (s *Service) bootstrapWindowsAdministrator(ctx context.Context, op *AuthenticationOperation, index int, hop routeHopState, interactive bool) (bool, error) {
	if op == nil {
		return s.detectWindowsAdministrator(ctx, hop, interactive)
	}
	op.runMu.Lock()
	defer op.runMu.Unlock()
	if err := op.check(ctx, index); err != nil {
		return false, err
	}
	run, _, err := op.runPhase(ctx, index, "ordinary", keySelector{}, ConnectionOptions{Args: []string{windowsAdminProbeProgram()}, Interactive: interactive, CaptureStdout: true, SuppressForwarding: true}, false, nil)
	if err != nil {
		return false, err
	}
	if run.ExitCode != 0 {
		return false, ErrInteractionRequired
	}
	switch strings.TrimSpace(string(run.Stdout)) {
	case "administrator":
		return true, nil
	case "standard":
		return false, nil
	default:
		return false, ErrManualRemediation
	}
}
