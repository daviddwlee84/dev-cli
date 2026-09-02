package sshhost

import (
	"context"
	"fmt"
)

// ProbeStatus is the stable result of one fresh ordinary alias login.
type ProbeStatus string

const (
	ProbeReady    ProbeStatus = "ready"
	ProbeNotReady ProbeStatus = "not_ready"
	ProbeError    ProbeStatus = "error"
)

// ProbeResult deliberately omits stderr and evaluated command-like options.
type ProbeResult struct {
	Alias    string      `json:"alias"`
	Status   ProbeStatus `json:"status"`
	Code     string      `json:"code"`
	Ready    bool        `json:"ready"`
	ExitCode int         `json:"exit_code,omitempty"`
}

// Probe performs one ordinary, noninteractive authentication proof using the
// system client. -S none prevents an existing ControlMaster from satisfying the
// proof; no host-key or known_hosts option is added or weakened.
func (s *Service) Probe(ctx context.Context, alias string) (ProbeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := ProbeResult{Alias: alias, Status: ProbeNotReady, Code: "not_ready"}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := validateRouteLookupAlias(alias); err != nil {
		return result, err
	}
	args := appendFreshSSHOptions(nil, true)
	args = append(args, alias, "exit 0")
	run, err := s.runner.Run(ctx, RunRequest{
		Name: "ssh", Args: args, Display: "ssh fresh authentication probe",
	})
	if err != nil {
		result.Status = ProbeError
		result.Code = "runner_error"
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, fmt.Errorf("run fresh SSH probe: %w", err)
	}
	result.ExitCode = run.ExitCode
	if run.ExitCode == 0 {
		result.Status = ProbeReady
		result.Code = "ready"
		result.Ready = true
	}
	return result, nil
}
