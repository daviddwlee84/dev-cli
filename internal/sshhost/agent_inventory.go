package sshhost

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

// AgentInventory is a public-metadata snapshot of one explicitly selected agent.
// A complete empty inventory is a valid baseline. It proves neither vault item
// creation nor authentication; new fingerprints are only newly visible keys.
type AgentInventory struct {
	Agent       AgentSocketRef `json:"agent"`
	Candidates  []KeyCandidate `json:"candidates,omitempty"`
	Complete    bool           `json:"complete"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
	state       *agentInventoryState
}

type agentInventoryState struct {
	serviceID uint64
	public    AgentInventory
	agent     keyAgentContext
}

// ObserveAgentKeys reads only the exact safe socket's public inventory. It does
// not inspect local identity files, ambient agents, hardware or provider vaults.
func (s *Service) ObserveAgentKeys(ctx context.Context, ref AgentSocketRef) (AgentInventory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return AgentInventory{Agent: ref}, err
	}
	identity, err := s.captureAgentSocket(ref.Socket)
	if err != nil {
		return AgentInventory{Agent: ref, Diagnostics: []Diagnostic{{Code: "agent_unavailable", Path: ref.Socket, Incomplete: true}}}, errors.Join(ErrAgentProviderUnavailable, err)
	}
	return s.observeAgentInventory(ctx, ref, keyAgentContext{socket: ref.Socket, identity: identity})
}

// RefreshAgentKeys requires a complete, unmodified baseline from this service
// and the same socket instance. A restarted or substituted agent requires a new
// explicit baseline; it is never treated as an empty previous key set.
func (s *Service) RefreshAgentKeys(ctx context.Context, snapshot AgentInventory) (AgentInventory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return AgentInventory{}, err
	}
	state := snapshot.state
	if state == nil || state.serviceID != s.id || !snapshot.Complete || !state.public.Complete {
		return AgentInventory{}, fmt.Errorf("agent refresh requires a complete service-bound baseline: %w", ErrBlocked)
	}
	if !reflect.DeepEqual(cloneAgentInventoryPublic(snapshot), state.public) {
		return AgentInventory{}, fmt.Errorf("agent inventory baseline was modified: %w", ErrSourceChanged)
	}
	return s.observeAgentInventory(ctx, state.public.Agent, state.agent)
}

func (s *Service) observeAgentInventory(ctx context.Context, ref AgentSocketRef, agent keyAgentContext) (AgentInventory, error) {
	inventory := AgentInventory{Agent: ref}
	records, diagnostics, err := s.readAgentKeys(ctx, &agent, "ssh-add exact-agent public inventory")
	if err != nil {
		inventory.Diagnostics = []Diagnostic{{Code: "agent_unavailable", Path: ref.Socket, Incomplete: true}}
		return inventory, err
	}
	for _, code := range diagnostics {
		inventory.Diagnostics = append(inventory.Diagnostics, Diagnostic{Code: code, Path: ref.Socket, Incomplete: true})
	}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if seen[record.metadata.Fingerprint] {
			continue
		}
		seen[record.metadata.Fingerprint] = true
		candidate := s.bindKeyMaterial(KeyCandidate{
			Source: KeySourceAgent, Sources: []KeySource{KeySourceAgent},
			Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment,
			Fingerprint: record.metadata.Fingerprint, Provenance: KeyProvenance{Agent: true}, Agent: cloneAgentRef(&ref),
		}, record.normalized)
		copy := agent
		candidate.state.agent = &copy
		inventory.Candidates = append(inventory.Candidates, candidate)
	}
	inventory.Complete = len(diagnostics) == 0
	inventory.state = &agentInventoryState{serviceID: s.id, agent: agent, public: cloneAgentInventoryPublic(inventory)}
	return inventory, nil
}

func cloneAgentInventoryPublic(inventory AgentInventory) AgentInventory {
	inventory.state = nil
	if inventory.Candidates != nil {
		candidates := make([]KeyCandidate, len(inventory.Candidates))
		for index, candidate := range inventory.Candidates {
			candidates[index] = cloneKeyCandidate(candidate)
		}
		inventory.Candidates = candidates
	}
	if inventory.Diagnostics != nil {
		diagnostics := make([]Diagnostic, len(inventory.Diagnostics))
		copy(diagnostics, inventory.Diagnostics)
		for index := range diagnostics {
			if diagnostics[index].Source != nil {
				copy := *diagnostics[index].Source
				diagnostics[index].Source = &copy
			}
		}
		inventory.Diagnostics = diagnostics
	}
	return inventory
}
