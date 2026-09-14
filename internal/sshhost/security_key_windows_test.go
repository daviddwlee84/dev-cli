//go:build windows

package sshhost

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsHardwareGenerationFailsClosedBeforeNativeExecution(t *testing.T) {
	paths := fixturePaths(t)
	s := newFixtureService(t, paths, DiscoverOptions{})
	tool := filepath.Join(paths.Home, "inert-native-tool.exe")
	writeFixture(t, tool, "inert executable fixture")
	// The tools exist, but the production attestation policy is not replaced by
	// the success seam used in Unix enrollment tests. The runner must not run.
	s.securityKeyLookPath = func(string) (string, error) { return tool, nil }
	observation, err := s.ObserveSecurityKeyCapability(t.Context(), "internal")
	if err != nil || observation.CanAttempt || !hasDiagnostic(observation.Diagnostics, "security_key_tool_unsafe") {
		t.Fatalf("unattested Windows tools accepted: %+v %v", observation, err)
	}
	for _, keyType := range []KeyType{KeyTypeEd25519SK, KeyTypeECDSASK} {
		plan, err := s.PlanKey(t.Context(), KeyRequest{Operation: KeyGenerate, Type: keyType, Interactive: true, DestinationIdentity: filepath.Join(paths.SSHDir, "must-not-exist")})
		if err != nil || plan.Ready() || plan.Action != ActionBlocked {
			t.Fatalf("Windows hardware plan ready: %+v %v", plan, err)
		}
		result, err := s.ApplyKey(t.Context(), plan)
		if !errors.Is(err, ErrBlocked) || result.Created || result.Hardware != nil {
			t.Fatalf("blocked Windows plan applied: %+v %v", result, err)
		}
	}
	entries, err := os.ReadDir(paths.SSHDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("blocked hardware request wrote files: %v %v", entries, err)
	}
	data, err := os.ReadFile(tool)
	if err != nil || string(data) != "inert executable fixture" {
		t.Fatalf("tool fixture changed: %v", err)
	}
}
