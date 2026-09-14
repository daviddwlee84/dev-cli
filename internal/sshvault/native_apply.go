package sshvault

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

func (s *Service) planBitwardenNative(ctx context.Context, request Request) (Plan, error) {
	if !request.NativeContextApproved {
		return Plan{}, ErrUnsupportedContext
	}
	execution, err := s.captureNativeContext(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer execution.release()
	destination, version, err := execution.observe(ctx, request)
	if err != nil {
		return Plan{}, err
	}
	current, err := s.captureNativeContext(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer current.release()
	if !sameNativeContext(execution.proof, current.proof) {
		return Plan{}, ErrStale
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Plan{}, ErrUnavailable
	}
	request.AccountID, request.VaultID = destination.AccountID, destination.VaultID
	proof, view := execution.proof, execution.proof.view
	state := &planState{service: s, request: request, dest: destination, version: version, operation: hex.EncodeToString(nonce[:]), native: &proof}
	return Plan{Destination: destination, Version: version, Title: request.Title, Experimental: true, NativeContextApproved: true, NativeProfile: &view, EndpointStatus: EndpointUnverified, state: state}, nil
}

func nativePlanMatches(plan Plan, state *planState) bool {
	if plan.NativeContextApproved != state.request.NativeContextApproved {
		return false
	}
	if state.native == nil {
		return plan.NativeProfile == nil && plan.EndpointStatus == ""
	}
	return plan.NativeProfile != nil && *plan.NativeProfile == state.native.view && plan.EndpointStatus == EndpointUnverified
}

// The caller holds Service.mu for the complete single-attempt operation.
func (s *Service) applyBitwardenNative(ctx context.Context, state *planState) (Result, error) {
	result := Result{Status: StatusNotStarted, BindingStatus: BindingUnknown, NativeContextStatus: NativeUnknown, EndpointStatus: EndpointUnverified}
	if !state.request.NativeContextApproved || !state.request.Experimental || state.native == nil {
		return result, ErrUnsupportedContext
	}
	execution, err := s.captureNativeContext(ctx)
	if err != nil {
		return result, err
	}
	defer execution.release()
	if !sameNativeContext(*state.native, execution.proof) {
		return result, ErrStale
	}
	destination, version, err := execution.observe(ctx, state.request)
	if err != nil {
		return result, err
	}
	if destination != state.dest || version != state.version {
		return result, ErrStale
	}
	// Catch ambient/profile/tool changes during preflight before generating any
	// private material. Execution still uses its original frozen context only.
	current, err := s.captureNativeContext(ctx)
	if err != nil {
		return result, err
	}
	unchanged := sameNativeContext(execution.proof, current.proof)
	current.release()
	if !unchanged {
		return result, ErrStale
	}
	result, createErr := s.createBitwardenUsing(ctx, state, execution.run)
	result.BindingStatus, result.NativeContextStatus, result.EndpointStatus = BindingUnknown, NativeUnknown, EndpointUnverified
	if result.Status != StatusCreated || errors.Is(createErr, ErrUnknown) {
		return result, createErr
	}
	observed, observedVersion, observationErr := execution.observe(ctx, state.request)
	post, contextErr := s.captureNativeContext(ctx)
	if post != nil {
		defer post.release()
	}
	if observationErr != nil || contextErr != nil || observed != state.dest || observedVersion != state.version || !sameNativeContext(execution.proof, post.proof) {
		return result, ErrUnknown
	}
	// Consistent native observations are not proof of an API/identity endpoint,
	// a vault server transaction, or availability through an SSH agent.
	result.NativeContextStatus = NativeObservedConsistent
	return result, createErr
}
