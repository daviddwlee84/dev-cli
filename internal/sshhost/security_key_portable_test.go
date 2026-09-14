package sshhost

import (
	"errors"
	"testing"
)

func TestSecurityKeyPublicAlgorithmsRemainPortable(t *testing.T) {
	for _, test := range []struct {
		kind KeyType
		line []byte
	}{
		{KeyTypeEd25519, testPublicLine(0xe1, "ordinary")},
		{KeyTypeEd25519SK, testSecurityKeyLine(0xe2, "security key")},
		{KeyTypeECDSASK, ecdsaSecurityKeyLine()},
	} {
		metadata, err := ParsePublicKey(test.line)
		if err != nil || metadata.Algorithm != test.kind.algorithm() {
			t.Fatalf("%s metadata=%+v err=%v", test.kind, metadata, err)
		}
	}
}

func TestAutomaticSecureEnclaveGenerationIsBlockedWithoutNativeLookup(t *testing.T) {
	s := newFixtureService(t, fixturePaths(t), DiscoverOptions{})
	s.securityKeyLookPath = func(string) (string, error) { t.Fatal("Secure Enclave refusal queried native tools"); return "", nil }
	observation, err := s.ObserveSecurityKeyCapability(t.Context(), "apple-secure-enclave")
	if err != nil || observation.CanAttempt || !hasDiagnostic(observation.Diagnostics, "secure_enclave_generation_unverified") {
		t.Fatalf("Secure Enclave observation=%+v err=%v", observation, err)
	}
	plan, err := s.PlanKey(t.Context(), KeyRequest{Operation: KeyGenerate, Type: KeyTypeECDSASK, SecurityKey: SecurityKeyOptions{Provider: "apple-secure-enclave"}, Interactive: true})
	if err != nil || plan.Ready() || plan.Action != ActionBlocked {
		t.Fatalf("Secure Enclave plan=%+v err=%v", plan, err)
	}
	if _, err := s.ApplyKey(t.Context(), plan); !errors.Is(err, ErrBlocked) {
		t.Fatalf("blocked Secure Enclave plan applied: %v", err)
	}
}
