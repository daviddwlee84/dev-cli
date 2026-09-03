package localfiles

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

type recordedCall struct {
	args  []string
	stdin []byte
	retry fleet.RetryPolicy
}

type recordingRunner struct {
	results []fleet.Result
	calls   []recordedCall
}

func (r *recordingRunner) RunWithOptions(_ context.Context, _ fleet.Host, args []string, stdin []byte, options fleet.RunOptions) fleet.Result {
	r.calls = append(r.calls, recordedCall{args: append([]string(nil), args...), stdin: append([]byte(nil), stdin...), retry: options.Retry})
	if len(r.results) == 0 {
		return fleet.Result{ExitCode: 127}
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result
}

func capabilityResult(t *testing.T, machineID string, supported bool, reason string) fleet.Result {
	t.Helper()
	request := fleet.LocalFilesCapabilityRequest()
	response := fleet.CapabilityResponse{
		SchemaVersion: request.SchemaVersion, Feature: request.Feature,
		ProtocolVersion: request.ProtocolVersion, MachineID: machineID,
		Platform: "linux", Supported: supported, Reason: reason,
		Limits: limitsToWire(safefileDefault()),
	}
	body, err := fleet.MarshalBounded(response, fleet.MaxCapabilityBytes)
	if err != nil {
		t.Fatal(err)
	}
	return fleet.Result{ExitCode: 0, Stdout: body}
}

func TestClientCapabilityPinMismatchStopsBeforeFurtherProtocol(t *testing.T) {
	runner := &recordingRunner{results: []fleet.Result{capabilityResult(t, testTargetMachine, true, "")}}
	client := Client{Runner: runner}
	host := fleet.Host{Name: "target", MachineID: testSourceMachine}
	if _, err := client.Probe(t.Context(), host); !errors.Is(err, ErrMachinePin) {
		t.Fatalf("pin mismatch error = %v", err)
	}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0].args, " ") != "fleet _capability" {
		t.Fatalf("calls = %+v", runner.calls)
	}
	if strings.Contains(string(runner.calls[0].stdin), "content_base64") {
		t.Fatal("capability request contained payload")
	}
}

func TestClientIdentifyReportsObservedMachineWithoutEnforcingPin(t *testing.T) {
	runner := &recordingRunner{results: []fleet.Result{capabilityResult(t, testTargetMachine, false, "native-windows-acl-transport-disabled")}}
	client := Client{Runner: runner}
	response, err := client.Identify(t.Context(), fleet.Host{Name: "target", MachineID: testSourceMachine})
	if err != nil {
		t.Fatal(err)
	}
	if response.MachineID != testTargetMachine || response.Supported || response.Reason != "native-windows-acl-transport-disabled" {
		t.Fatalf("identity response = %+v", response)
	}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0].args, " ") != "fleet _capability" {
		t.Fatalf("calls = %+v", runner.calls)
	}
}

func TestClientUnpinnedApplyNeverInvokesTransport(t *testing.T) {
	runner := &recordingRunner{}
	client := Client{Runner: runner}
	_, err := client.Apply(t.Context(), fleet.Host{Name: "target"}, fleet.CapabilityResponse{MachineID: testTargetMachine}, ApplyEnvelope{})
	if !errors.Is(err, ErrMachineUnpinned) || len(runner.calls) != 0 {
		t.Fatalf("unpinned apply = %v; calls=%d", err, len(runner.calls))
	}
}

func TestClientDoesNotExposeRemoteStderrOrUnsupportedReason(t *testing.T) {
	secret := "TOKEN=never-print-this"
	runner := &recordingRunner{results: []fleet.Result{{ExitCode: 2, Stderr: []byte(secret)}}}
	client := Client{Runner: runner}
	_, err := client.Probe(t.Context(), fleet.Host{Name: "target"})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error leaked stderr: %v", err)
	}

	runner = &recordingRunner{results: []fleet.Result{capabilityResult(t, testTargetMachine, false, secret)}}
	client = Client{Runner: runner}
	_, err = client.Probe(t.Context(), fleet.Host{Name: "target"})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsupported reason leaked: %v", err)
	}
}

func safefileDefault() safefile.Limits { return safefile.DefaultLimits() }
