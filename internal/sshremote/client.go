package sshremote

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
)

type Runner interface {
	RunWithOptions(context.Context, fleet.Host, []string, []byte, fleet.RunOptions) fleet.Result
}
type InteractiveRunner interface {
	InteractiveNoForward(context.Context, fleet.Host, []string, bool) error
}

// Client never recursively discovers remote fleets. Callers explicitly select
// every Host, and caching remains a separate local action. AllowPrompt permits
// only an already configured password source, never a new credential prompt.
type Client struct {
	Runner      Runner
	AllowPrompt bool
	Timeout     time.Duration
	onResult    func(fleet.Result)
}

func (c Client) run(ctx context.Context, host fleet.Host, helper string, request, response any, limit int64) error {
	if c.Runner == nil {
		return ErrUnavailable
	}
	body, err := fleet.MarshalBounded(request, MaxRequestBytes)
	if err != nil {
		return ErrInvalidData
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	promptSuppressed := host.PasswordKind() == "prompt" && !c.AllowPrompt
	retry := fleet.RetryAuthentication
	if promptSuppressed {
		// Retain the explicit source so the transport cannot fall through to a
		// stored credential callback. Only disable the password retry.
		retry = fleet.RetryNever
	}
	result := c.Runner.RunWithOptions(ctx, host, []string{"fleet", helper}, body, fleet.RunOptions{Retry: retry})
	if c.onResult != nil {
		c.onResult(result)
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ErrTimeout
		}
		return err
	}
	if result.TimedOut || result.ExitCode == 124 {
		return ErrTimeout
	}
	if result.CaptureError != "" || int64(len(result.Stdout)) > limit {
		return ErrInvalidData
	}
	switch result.ExitCode {
	case 0:
	case 127:
		return ErrUnavailable
	case 255:
		if promptSuppressed {
			return ErrInteractionRequired
		}
		return ErrUnreachable
	default:
		return ErrIncompatible
	}
	var discriminator struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(result.Stdout, &discriminator) != nil {
		return ErrInvalidData
	}
	if discriminator.Kind == "ssh_remote_error" {
		var failure ErrorResponse
		if fleet.UnmarshalStrict(result.Stdout, limit, &failure) != nil || failure.Header.Validate() != nil {
			return ErrInvalidData
		}
		return remoteError(failure.Code)
	}
	if fleet.UnmarshalStrict(result.Stdout, limit, response) != nil {
		return ErrInvalidData
	}
	return nil
}

func remoteError(code string) error {
	switch code {
	case StatusUnavailable:
		return ErrUnavailable
	case StatusIncompatible:
		return ErrIncompatible
	case StatusUnreachable:
		return ErrUnreachable
	case StatusTimeout:
		return ErrTimeout
	case StatusStale:
		return ErrSourceChanged
	case "not-found":
		return ErrNotFound
	case "interaction-required":
		return ErrInteractionRequired
	case "unsupported":
		return ErrUnsupported
	default:
		return ErrInvalidData
	}
}

func (c Client) Capability(ctx context.Context, host fleet.Host) (Capability, error) {
	var response Capability
	if err := c.run(ctx, host, "_ssh-capability", CapabilityRequest{Header: NewHeader()}, &response, fleet.MaxCapabilityBytes); err != nil {
		return response, err
	}
	if err := response.Validate(); err != nil {
		return Capability{}, err
	}
	if host.MachineID != "" && host.MachineID != response.Origin.MachineID {
		return response, ErrSourceChanged
	}
	return response, nil
}

func (c Client) Inventory(ctx context.Context, host fleet.Host) (Inventory, error) {
	capability, err := c.Capability(ctx, host)
	if err != nil {
		return Inventory{}, err
	}
	var response Inventory
	if err := c.run(ctx, host, "_ssh-inventory", NewRequest(capability.Origin), &response, MaxResponseBytes); err != nil {
		return response, err
	}
	if response.Origin != capability.Origin {
		return Inventory{}, ErrSourceChanged
	}
	return response, response.Validate()
}

func (c Client) Resolve(ctx context.Context, host fleet.Host, selection Selection) (Resolved, error) {
	if err := selection.Validate(); err != nil {
		return Resolved{}, err
	}
	capability, err := c.Capability(ctx, host)
	if err != nil {
		return Resolved{}, err
	}
	if selection.OriginID != capability.Origin.ID {
		return Resolved{}, ErrSourceChanged
	}
	var response Resolved
	request := ResolveRequest{Request: NewRequest(capability.Origin), Selection: selection}
	if err := c.run(ctx, host, "_ssh-resolve", request, &response, MaxResponseBytes); err != nil {
		return response, err
	}
	if response.Origin != capability.Origin || response.Profile.Selection(response.Origin) != selection {
		return Resolved{}, ErrSourceChanged
	}
	return response, response.Validate()
}

func (c Client) Keys(ctx context.Context, host fleet.Host, alias string, noAgent bool) (Keys, error) {
	capability, err := c.Capability(ctx, host)
	if err != nil {
		return Keys{}, err
	}
	var response Keys
	request := KeysRequest{Request: NewRequest(capability.Origin), Alias: alias, NoAgent: noAgent}
	if err := c.run(ctx, host, "_ssh-keys", request, &response, MaxResponseBytes); err != nil {
		return response, err
	}
	if response.Origin != capability.Origin || response.Alias != alias {
		return Keys{}, ErrSourceChanged
	}
	return response, response.Validate()
}

// Connect enters a single fixed helper over an interactive, nonforwarding SSH
// connection. It does not use the metadata retry path for session execution.
func (c Client) Connect(ctx context.Context, host fleet.Host, selection Selection, keyID string, usePassword bool) error {
	runner, ok := c.Runner.(InteractiveRunner)
	if !ok {
		return ErrUnavailable
	}
	// Reuse the authentication method that actually worked for this fresh
	// capability exchange. Merely configuring a password source must not make a
	// working key login depend on an available/unlocked password provider.
	probe := c
	previous := c.onResult
	probe.onResult = func(result fleet.Result) {
		usePassword = usePassword || result.UsedPassword
		if previous != nil {
			previous(result)
		}
	}
	capability, err := probe.Capability(ctx, host)
	if err != nil {
		return err
	}
	if selection.OriginID != capability.Origin.ID {
		return ErrSourceChanged
	}
	request := ConnectRequest{Request: NewRequest(capability.Origin), Selection: selection, KeyID: keyID}
	encoded, err := EncodeConnectRequest(request)
	if err != nil {
		return err
	}
	return runner.InteractiveNoForward(ctx, host, []string{"fleet", "_ssh-connect", "--request", encoded}, usePassword)
}
