package sshhost

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

func hardwareGateAuthentication(operation *AuthenticationOperation, index int, selector keySelector, interactive bool) bootstrapAuthentication {
	gate := bootstrapAuthentication{operation: operation, index: index}
	if interactive && selector.securityKey {
		copy := selector
		copy.ordinaryHardwareGate = true
		gate.hardware = &copy
	}
	return gate
}

// A hardware ordinary gate is a fresh login with native identity selection, not
// another selected-key proof. Native interaction cannot add account-password or
// keyboard-interactive fallback merely because a hardware key needs a PIN.
func writeHardwareOrdinaryAuthentication(body *strings.Builder, effective EffectiveConfig, selector keySelector) error {
	if selector.hardware != nil {
		if !securityKeyProviderMatches(effective, selector.hardware) {
			return errors.Join(ErrManualRemediation, ErrSecurityKeyPolicyMismatch)
		}
		writeConfigDirective(body, "SecurityKeyProvider", selector.hardware.options.Provider)
	}
	for _, item := range []struct{ name, value string }{
		{"PreferredAuthentications", "publickey"}, {"PasswordAuthentication", "no"},
		{"KbdInteractiveAuthentication", "no"}, {"ChallengeResponseAuthentication", "no"},
		{"GSSAPIAuthentication", "no"}, {"HostbasedAuthentication", "no"},
	} {
		writeConfigDirective(body, item.name, item.value)
	}
	return writeOrdinaryProxyAuthentication(body, effective)
}

func (s *Service) revalidateHardwareGateNativeConfig(ctx context.Context, hop routeHopState) error {
	route := hop.proofRoute
	if len(route) == 0 {
		route = []proofRouteHop{{alias: hop.safe.Alias, effective: hop.effective}}
	}
	for _, entry := range route {
		current, err := s.Effective(ctx, entry.alias)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, entry.effective) {
			return fmt.Errorf("native SSH configuration changed before the hardware ordinary gate: %w", ErrSourceChanged)
		}
	}
	return nil
}
