package localfiles

import (
	"context"
	"errors"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
)

var (
	ErrNoRemoteDev     = errors.New("target does not provide a compatible dev protocol")
	ErrUnreachable     = errors.New("target host is unreachable")
	ErrRemoteTimeout   = errors.New("target protocol timed out")
	ErrMachinePin      = errors.New("target machine ID does not match the configured host pin")
	ErrMachineUnpinned = errors.New("mutating local-file apply requires host.machine_id")
)

type Runner interface {
	RunWithOptions(context.Context, fleet.Host, []string, []byte, fleet.RunOptions) fleet.Result
}

type Client struct{ Runner Runner }

func (c Client) run(ctx context.Context, host fleet.Host, args []string, stdin []byte, maxResponse int64) ([]byte, error) {
	if c.Runner == nil {
		return nil, errors.New("local-files client has no fleet transport")
	}
	// Capability and plan are read-only. Apply is permanently bound to its
	// request ID and plan digest by the target journal, so an authentication-only
	// retry cannot duplicate an unbound mutation.
	result := c.Runner.RunWithOptions(ctx, host, args, stdin, fleet.RunOptions{Retry: fleet.RetryAuthentication})
	if int64(len(result.Stdout)) > maxResponse {
		return nil, errors.New("target protocol response exceeded its bound")
	}
	if result.ExitCode == 0 && result.CaptureError == "" {
		return result.Stdout, nil
	}
	switch {
	case result.TimedOut || result.ExitCode == 124:
		return nil, ErrRemoteTimeout
	case result.ExitCode == 127:
		return nil, ErrNoRemoteDev
	case result.ExitCode == 255:
		return nil, ErrUnreachable
	default:
		return nil, ErrNoRemoteDev
	}
}

// Identify performs the content-free capability exchange without enforcing the
// configured machine pin. It exists for explicit onboarding and diagnostics;
// mutating or payload-bearing callers must use Probe.
func (c Client) Identify(ctx context.Context, host fleet.Host) (fleet.CapabilityResponse, error) {
	request := fleet.LocalFilesCapabilityRequest()
	body, err := fleet.MarshalBounded(request, fleet.MaxCapabilityBytes)
	if err != nil {
		return fleet.CapabilityResponse{}, err
	}
	responseBody, err := c.run(ctx, host, []string{"fleet", "_capability"}, body, fleet.MaxCapabilityBytes)
	if err != nil {
		return fleet.CapabilityResponse{}, err
	}
	var response fleet.CapabilityResponse
	if err := fleet.UnmarshalStrict(responseBody, fleet.MaxCapabilityBytes, &response); err != nil {
		return fleet.CapabilityResponse{}, ErrNoRemoteDev
	}
	if err := response.Validate(request); err != nil {
		return fleet.CapabilityResponse{}, ErrNoRemoteDev
	}
	return response, nil
}

func (c Client) Probe(ctx context.Context, host fleet.Host) (fleet.CapabilityResponse, error) {
	response, err := c.Identify(ctx, host)
	if err != nil {
		return fleet.CapabilityResponse{}, err
	}
	if host.MachineID != "" && host.MachineID != response.MachineID {
		return fleet.CapabilityResponse{}, ErrMachinePin
	}
	if !response.Supported {
		return fleet.CapabilityResponse{}, errors.New("target does not support local-file payloads")
	}
	return response, nil
}

func (c Client) Plan(ctx context.Context, host fleet.Host, capability fleet.CapabilityResponse, request PlanRequest) (PlanResponse, error) {
	if capability.MachineID != request.Binding.TargetMachine {
		return PlanResponse{}, ErrMachinePin
	}
	if err := request.Validate(); err != nil {
		return PlanResponse{}, err
	}
	body, err := fleet.MarshalBounded(request, MaxPlanEnvelopeBytes)
	if err != nil {
		return PlanResponse{}, err
	}
	responseBody, err := c.run(ctx, host, []string{"fleet", "_files-plan"}, body, MaxPlanEnvelopeBytes)
	if err != nil {
		return PlanResponse{}, err
	}
	var response PlanWireResponse
	if err := fleet.UnmarshalStrict(responseBody, MaxPlanEnvelopeBytes, &response); err != nil {
		return PlanResponse{}, ErrNoRemoteDev
	}
	if err := response.Validate(request); err != nil {
		return PlanResponse{}, ErrNoRemoteDev
	}
	if response.ErrorCode != "" {
		return PlanResponse{}, &TargetError{Code: response.ErrorCode}
	}
	return *response.Plan, nil
}

func (c Client) Apply(ctx context.Context, host fleet.Host, capability fleet.CapabilityResponse, envelope ApplyEnvelope) (ApplyResponse, error) {
	if host.MachineID == "" {
		return ApplyResponse{}, ErrMachineUnpinned
	}
	if host.MachineID != capability.MachineID || envelope.Request.Binding.TargetMachine != capability.MachineID {
		return ApplyResponse{}, ErrMachinePin
	}
	if err := envelope.Validate(); err != nil {
		return ApplyResponse{}, err
	}
	body, err := fleet.MarshalBounded(envelope, MaxApplyEnvelopeBytes)
	if err != nil {
		return ApplyResponse{}, err
	}
	responseBody, err := c.run(ctx, host, []string{"fleet", "_files-apply"}, body, MaxApplyResponseBytes)
	if err != nil {
		return ApplyResponse{}, err
	}
	var response ApplyResponse
	if err := fleet.UnmarshalStrict(responseBody, MaxApplyResponseBytes, &response); err != nil {
		return ApplyResponse{}, ErrNoRemoteDev
	}
	if err := response.Validate(envelope.Request, envelope.Plan); err != nil {
		return ApplyResponse{}, ErrNoRemoteDev
	}
	return response, nil
}
