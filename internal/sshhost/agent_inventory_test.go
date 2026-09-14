package sshhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type exactInventoryFixture struct {
	*agentKeyFixture
	output      RunResult
	err         error
	calls       int
	duringQuery func()
}

func newExactInventoryFixture(t *testing.T) *exactInventoryFixture {
	t.Helper()
	f := &exactInventoryFixture{agentKeyFixture: newAgentKeyFixture(t), output: RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n")}}
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(f.service.paths.Home, "ambient-must-not-be-queried.sock"))
	f.service.runner = keyCatalogRunnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		f.calls++
		if request.Name != "ssh-add" || !reflect.DeepEqual(request.Args, []string{"-L"}) || !reflect.DeepEqual(request.Env, []string{"LC_ALL=C", "SSH_AUTH_SOCK=" + f.ref.Socket}) {
			t.Fatalf("inventory queried another source: %+v", request)
		}
		if f.duringQuery != nil {
			f.duringQuery()
		}
		return f.output, f.err
	})
	return f
}

func TestAgentInventoryCompleteEmptyBaselineRefreshesNewVisibleKeys(t *testing.T) {
	f := newExactInventoryFixture(t)
	private := filepath.Join(f.service.paths.SSHDir, "local-only")
	writeFixture(t, private, "opaque local private material")
	writeFixture(t, private+".pub", string(testPublicLine(0xd1, "local-only"))+"\n")
	baseline, err := f.service.ObserveAgentKeys(t.Context(), f.ref)
	if err != nil || !baseline.Complete || len(baseline.Candidates) != 0 || f.calls != 1 {
		t.Fatalf("baseline=%+v err=%v calls=%d", baseline, err, f.calls)
	}
	second := testPublicLine(0xd2, "newly visible")
	f.output = RunResult{Stdout: []byte(string(f.line) + "\n" + string(second) + "\n" + string(second) + "\n")}
	fresh, err := f.service.RefreshAgentKeys(t.Context(), baseline)
	if err != nil || !fresh.Complete || len(fresh.Candidates) != 2 || f.calls != 2 {
		t.Fatalf("refresh=%+v err=%v calls=%d", fresh, err, f.calls)
	}
	if len(baseline.Candidates) != 0 {
		t.Fatal("refresh mutated the empty baseline")
	}
	for _, candidate := range fresh.Candidates {
		if candidate.Agent == nil || *candidate.Agent != f.ref || candidate.PublicPath != "" || candidate.IdentityFile != "" || candidate.Provenance.Private || !candidate.Provenance.Agent {
			t.Fatalf("inventory inherited local authority: %+v", candidate)
		}
		plan, err := f.service.PlanKey(t.Context(), KeyRequest{Candidate: candidate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.ApplyKey(t.Context(), plan); err != nil {
			t.Fatalf("fresh candidate was not service-bound: %v", err)
		}
	}
	encoded, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, f.line) || bytes.Contains(encoded, second) || bytes.Contains(encoded, []byte("opaque local private")) {
		t.Fatal("inventory serialized key material")
	}
	f.output = RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n")}
	emptyAgain, err := f.service.RefreshAgentKeys(t.Context(), fresh)
	if err != nil || !emptyAgain.Complete || len(emptyAgain.Candidates) != 0 {
		t.Fatalf("known empty refreshed inventory=%+v err=%v", emptyAgain, err)
	}
}

func TestAgentInventoryFailuresNeverBecomeEmptyBaselines(t *testing.T) {
	for _, test := range []struct {
		name     string
		output   RunResult
		err      error
		complete bool
	}{
		{"successful-empty", RunResult{}, nil, true},
		{"native-empty", RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n")}, nil, true},
		{"generic-exit-one", RunResult{ExitCode: 1}, nil, false},
		{"fetch-failed", RunResult{ExitCode: 1, Stderr: []byte("error fetching identities")}, nil, false},
		{"empty-plus-error", RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n"), Stderr: []byte("error")}, nil, false},
		{"stderr-only-empty", RunResult{ExitCode: 1, Stderr: []byte("The agent has no identities.\n")}, nil, false},
		{"unavailable", RunResult{ExitCode: 2}, nil, false},
		{"truncated-empty", RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n"), StdoutTruncated: true}, nil, false},
		{"truncated-stderr", RunResult{StderrTruncated: true}, nil, false},
		{"invalid-public-record", RunResult{Stdout: []byte("invalid public record")}, nil, false},
		{"launch-failed", RunResult{}, errors.New("sensitive native failure text"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newExactInventoryFixture(t)
			f.output, f.err = test.output, test.err
			inventory, err := f.service.ObserveAgentKeys(t.Context(), f.ref)
			if err != nil || inventory.Complete != test.complete {
				t.Fatalf("inventory=%+v err=%v", inventory, err)
			}
			encoded, _ := json.Marshal(inventory)
			if strings.Contains(string(encoded), "sensitive native") || strings.Contains(string(encoded), "error fetching") {
				t.Fatal("native failure text leaked")
			}
			if !test.complete {
				if len(inventory.Diagnostics) == 0 {
					t.Fatal("incomplete inventory omitted diagnostics")
				}
				before := f.calls
				inventory.Complete = true
				if _, err := f.service.RefreshAgentKeys(t.Context(), inventory); !errors.Is(err, ErrBlocked) || f.calls != before {
					t.Fatalf("failed baseline promoted to complete: %v", err)
				}
			}
		})
	}
}

func TestAgentInventoryRejectsMutatedOrForeignBaselineBeforeQuery(t *testing.T) {
	for _, mutation := range []struct {
		name   string
		change func(*AgentInventory)
	}{
		{"provider", func(s *AgentInventory) { s.Agent.Provider = AgentProvider1Password }},
		{"socket", func(s *AgentInventory) { s.Agent.Socket += ".other" }},
		{"fingerprint", func(s *AgentInventory) { s.Candidates[0].Fingerprint = "SHA256:forged" }},
		{"candidate-agent", func(s *AgentInventory) { s.Candidates[0].Agent.Socket += ".other" }},
		{"sources", func(s *AgentInventory) { s.Candidates[0].Sources[0] = KeySourceGenerated }},
		{"provenance", func(s *AgentInventory) { s.Candidates[0].Provenance.Private = true }},
		{"removed-baseline", func(s *AgentInventory) { s.Candidates = nil }},
		{"added-key", func(s *AgentInventory) { s.Candidates = append(s.Candidates, s.Candidates[0]) }},
		{"diagnostics", func(s *AgentInventory) { s.Diagnostics = []Diagnostic{{Code: "caller diagnostic"}} }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := newExactInventoryFixture(t)
			f.output = RunResult{Stdout: f.line}
			baseline, err := f.service.ObserveAgentKeys(t.Context(), f.ref)
			if err != nil {
				t.Fatal(err)
			}
			mutation.change(&baseline)
			if _, err := f.service.RefreshAgentKeys(t.Context(), baseline); !errors.Is(err, ErrSourceChanged) || f.calls != 1 {
				t.Fatalf("mutated baseline queried: %v calls=%d", err, f.calls)
			}
		})
	}
	f := newExactInventoryFixture(t)
	baseline, err := f.service.ObserveAgentKeys(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewService(f.service.paths, panicRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.RefreshAgentKeys(t.Context(), baseline); !errors.Is(err, ErrBlocked) {
		t.Fatalf("foreign service baseline accepted: %v", err)
	}
	data, _ := json.Marshal(baseline)
	var unbound AgentInventory
	if err := json.Unmarshal(data, &unbound); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RefreshAgentKeys(t.Context(), unbound); !errors.Is(err, ErrBlocked) || f.calls != 1 {
		t.Fatalf("JSON restored snapshot authority: %v", err)
	}
}

func TestAgentInventoryKeepsOriginalSocketInstance(t *testing.T) {
	for _, phase := range []string{"before-refresh", "during-observe", "during-refresh"} {
		t.Run(phase, func(t *testing.T) {
			f := newExactInventoryFixture(t)
			replace := func() {
				if err := os.Rename(f.ref.Socket, f.ref.Socket+".old"); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, f.ref.Socket, "")
			}
			if phase == "during-observe" {
				f.duringQuery = replace
				inventory, err := f.service.ObserveAgentKeys(t.Context(), f.ref)
				if !errors.Is(err, ErrSourceChanged) || inventory.Complete {
					t.Fatalf("changed observing socket accepted: %+v %v", inventory, err)
				}
				return
			}
			baseline, err := f.service.ObserveAgentKeys(t.Context(), f.ref)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "before-refresh" {
				replace()
			} else {
				f.duringQuery = replace
			}
			inventory, err := f.service.RefreshAgentKeys(t.Context(), baseline)
			if !errors.Is(err, ErrSourceChanged) || inventory.Complete {
				t.Fatalf("changed refresh socket accepted: %+v %v", inventory, err)
			}
			if phase == "before-refresh" && f.calls != 1 {
				t.Fatal("replacement was queried before rejection")
			}
		})
	}
}

func TestAgentInventoryMissingSocketAndCanceledRefreshRemainIncomplete(t *testing.T) {
	f := newExactInventoryFixture(t)
	baseline, err := f.service.ObserveAgentKeys(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	inventory, err := f.service.RefreshAgentKeys(ctx, baseline)
	if !errors.Is(err, context.Canceled) || inventory.Complete || f.calls != 1 {
		t.Fatalf("canceled refresh queried: %+v %v", inventory, err)
	}
	if err := os.Remove(f.ref.Socket); err != nil {
		t.Fatal(err)
	}
	inventory, err = f.service.ObserveAgentKeys(t.Context(), f.ref)
	if !errors.Is(err, ErrAgentProviderUnavailable) || inventory.Complete || f.calls != 1 {
		t.Fatalf("missing socket became empty: %+v %v", inventory, err)
	}
}

func TestAgentCatalogDoesNotTreatFailedExitOneAsEmpty(t *testing.T) {
	f := newExactInventoryFixture(t)
	request := KeyCatalogRequest{Effective: &EffectiveConfig{Alias: "target", Values: map[string][]string{"identityagent": {f.ref.Socket}}}, Agents: []AgentSocketRef{f.ref}}
	f.output = RunResult{ExitCode: 1}
	catalog, err := f.service.Catalog(t.Context(), request)
	if err != nil || catalog.Complete || !hasDiagnostic(catalog.Diagnostics, "agent_unavailable") {
		t.Fatalf("failed catalog looked empty: %+v %v", catalog, err)
	}
	f.output = RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n")}
	catalog, err = f.service.Catalog(t.Context(), request)
	if err != nil || !catalog.Complete || len(catalog.Candidates) != 0 {
		t.Fatalf("known empty catalog=%+v %v", catalog, err)
	}
}

func TestAgentInventoryDiagnosticCloneDoesNotShareSourcePointers(t *testing.T) {
	original := AgentInventory{Diagnostics: []Diagnostic{{Code: "fixture", Source: &Location{Path: "original"}}}}
	copy := cloneAgentInventoryPublic(original)
	copy.Diagnostics[0].Source.Path = "changed"
	if original.Diagnostics[0].Source.Path != "original" {
		t.Fatal("diagnostic source pointer shared")
	}
}
