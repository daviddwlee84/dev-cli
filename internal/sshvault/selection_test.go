package sshvault

import (
	"context"
	"encoding/json"
	"testing"
)

func readyOnePasswordSelection(t *testing.T) (Plan, Result) {
	t.Helper()
	line, _ := fixturePublic(t)
	steps := append(opObservation(""), opObservation(opAccount)...)
	steps = append(steps, successfulOPCreate("test SSH key", nil), opPublicStep(map[string]any{"id": "public_key", "type": "STRING", "value": line}))
	steps = append(steps, opObservation(opAccount)...)
	runner := &scriptedRunner{t: t, steps: steps}
	service := NewService(runner)
	plan, err := service.Plan(context.Background(), opRequest())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	runner.verify(t)
	return plan, result
}

func TestAgentSelectionRequiresUnchangedSuccessfulApply(t *testing.T) {
	plan, result := readyOnePasswordSelection(t)
	if !CanSelectAgentKey(plan, result) {
		t.Fatal("valid 1Password receipt could not be matched to agent inventory")
	}
	for _, mutate := range []func(*Result){
		func(r *Result) { r.state = nil },
		func(r *Result) { r.Status = StatusUnknown },
		func(r *Result) { r.BindingStatus = BindingUnknown },
		func(r *Result) { r.NativeContextStatus = NativeObservedConsistent },
		func(r *Result) { r.EndpointStatus = EndpointUnverified },
		func(r *Result) { r.Receipt.ItemID = opVault },
		func(r *Result) { r.Receipt.Fingerprint = "SHA256:other" },
		func(r *Result) { r.Receipt.PublicLine += " modified" },
		func(r *Result) { r.Receipt.AccountID = opItem },
		func(r *Result) { r.Receipt = nil },
	} {
		changed := result
		receipt := *result.Receipt
		changed.Receipt = &receipt
		mutate(&changed)
		if CanSelectAgentKey(plan, changed) {
			t.Fatal("modified Apply receipt acquired agent-selection authority")
		}
	}
	changedPlan := plan
	changedPlan.Title = "other"
	if CanSelectAgentKey(changedPlan, result) {
		t.Fatal("modified plan acquired agent-selection authority")
	}
	encoded, _ := json.Marshal(result)
	var decoded Result
	if json.Unmarshal(encoded, &decoded) != nil || CanSelectAgentKey(plan, decoded) {
		t.Fatal("serialized result carried agent-selection authority")
	}
	encoded, _ = json.Marshal(plan)
	var decodedPlan Plan
	if json.Unmarshal(encoded, &decodedPlan) != nil || CanSelectAgentKey(decodedPlan, result) {
		t.Fatal("serialized plan carried agent-selection authority")
	}
	other, _ := readyOnePasswordSelection(t)
	if CanSelectAgentKey(other, result) {
		t.Fatal("receipt was accepted for a different source-bound plan")
	}
}

func TestPrivateBitwardenTestBackendCannotAuthorizeAgentSelection(t *testing.T) {
	runner := &scriptedRunner{t: t, steps: []runStep{successfulBWCreate(t, nil)}}
	service := NewService(runner)
	state := bitwardenBackendState(service, bwRequest())
	result, err := service.createBitwarden(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{Destination: state.dest, Version: state.version, Title: state.request.Title, Experimental: true, NativeContextApproved: true, state: state}
	if CanSelectAgentKey(plan, result) {
		t.Fatal("private fake creation bypassed public native-context guards")
	}
	runner.verify(t)
}
