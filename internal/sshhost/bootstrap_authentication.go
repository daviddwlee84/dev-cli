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

func (s *Service) bootstrapProof(ctx context.Context, op *AuthenticationOperation, index int, hop routeHopState, selector keySelector, exact bool) (bool, error) {
	if op == nil {
		return s.runSSHProof(ctx, hop, selector, exact)
	}
	op.runMu.Lock()
	defer op.runMu.Unlock()
	if err := op.check(ctx, index); err != nil {
		return false, err
	}
	mode := "ordinary"
	if exact {
		mode = "selected"
		if selector.agent != nil {
			configured, enabled, err := s.resolveIdentityAgent(firstEffectiveValue(hop.effective, "identityagent"))
			if err != nil || !enabled || configured != "" && configured != selector.agent.socket {
				return false, errors.Join(ErrUnprovenAuthentication, ErrAgentPolicyMismatch)
			}
			if err := s.revalidateAgentKey(ctx, selector.agent, selector.fingerprint); err != nil {
				return false, err
			}
		}
	}
	run, log, err := op.runPhase(ctx, index, mode, selector, ConnectionOptions{Args: []string{"exit 0"}, SuppressForwarding: true}, true, nil)
	if err != nil {
		return false, err
	}
	if exact {
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
