//go:build windows

package localfiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
)

func TestNativeWindowsCapabilityAndMutationsFailClosed(t *testing.T) {
	service := NewService(config.Default())
	service.StoreRoot = filepath.Join(t.TempDir(), "must-not-exist")
	service.LoadMachineID = func(context.Context) (string, error) { return testTargetMachine, nil }
	capability, err := service.Capability(t.Context(), fleet.LocalFilesCapabilityRequest())
	if err != nil || capability.Platform != "windows" || capability.Supported || capability.Reason != "native-windows-acl-transport-disabled" {
		t.Fatalf("native capability=%+v error=%v", capability, err)
	}
	service.LoadMachineID = func(context.Context) (string, error) {
		t.Fatal("unsupported mutation initialized identity")
		return "", nil
	}
	service.Fault = func(string, string) error { t.Fatal("unsupported mutation entered publisher"); return nil }
	if _, err := service.Plan(t.Context(), PlanRequest{}); !targetErrorIs(err, TargetIncompatible) {
		t.Fatalf("native plan error=%v", err)
	}
	if _, err := service.Apply(t.Context(), ApplyEnvelope{}); !targetErrorIs(err, TargetIncompatible) {
		t.Fatalf("native apply error=%v", err)
	}
	if _, err := os.Lstat(service.StoreRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported mutation created state: %v", err)
	}
}
