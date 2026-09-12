package sshcredential

import (
	"context"
	"reflect"
	"time"
)

type SaveDecision string

const (
	SaveYes   SaveDecision = "yes"
	SaveNo    SaveDecision = "no"
	SaveNever SaveDecision = "never"
)

type SaveResult struct {
	Status string `json:"status"`
	Record Record `json:"record"`
}
type Manager struct {
	Store     *Store
	Providers map[string]Provider
}

// Save is the persistence boundary called only by the authentication flow after
// its opaque, context-bound password proof. It deliberately accepts no public
// boolean or serialized "verified" field that could masquerade as such proof.
// SaveNo has no durable effect. SaveNever persists only the per-context policy.
func (m Manager) Save(ctx context.Context, c Context, secret []byte, decision SaveDecision, providerID string) (SaveResult, error) {
	r := SaveResult{Status: "not_saved"}
	if c.Validate() != nil {
		return r, ErrUnsafe
	}
	if decision == SaveNo {
		return r, nil
	}
	if m.Store == nil {
		return r, ErrUnavailable
	}
	if decision == SaveNever {
		record, e := m.Store.SavePreference(ctx, c, PolicyNever)
		r.Record = record
		r.Status = "never"
		return r, e
	}
	if decision != SaveYes || validateSecret(secret) != nil {
		return r, ErrUnsafe
	}
	provider := m.Providers[providerID]
	if provider == nil {
		return r, ErrUnavailable
	}
	if e := provider.Available(ctx); e != nil {
		return r, e
	}
	record, _, e := m.Store.Lookup(ctx, c)
	if e != nil {
		return r, e
	}
	if record.State != "ready" {
		return r, ErrUnknown
	}
	if record.Policy == PolicyNever {
		return r, ErrDenied
	}
	if record.Reference != nil && record.Reference.Provider != providerID {
		return r, ErrUnsafe
	}
	record.State = "pending"
	record.PendingProvider = providerID
	plan, e := m.Store.Plan(ctx, record)
	if e != nil {
		return r, e
	}
	pending, e := m.Store.Apply(ctx, plan)
	if e != nil {
		return r, e
	}
	r.Record = record
	r.Status = "pending"
	ref, e := provider.Put(ctx, c, record.Reference, secret)
	if e != nil {
		record.State = "unknown"
		r.Status = "unknown"
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if plan, pe := m.Store.Plan(cleanup, record); pe == nil && reflect.DeepEqual(plan.Before, pending) {
			_, _ = m.Store.Apply(cleanup, plan)
		}
		r.Record = record
		return r, ErrUnknown
	}
	if !validReference(ref) || ref.Provider != providerID {
		r.Status = "unknown"
		return r, ErrUnknown
	}
	record.Reference = &ref
	record.State = "ready"
	record.PendingProvider = ""
	plan, e = m.Store.Plan(ctx, record)
	if e == nil && !reflect.DeepEqual(plan.Before, pending) {
		e = ErrStale
	}
	if e == nil {
		_, e = m.Store.Apply(ctx, plan)
	}
	if e != nil {
		r.Status = "unknown"
		record.State = "unknown"
		r.Record = record
		return r, ErrUnknown
	}
	r.Status = "saved"
	r.Record = record
	return r, nil
}
func (m Manager) Get(ctx context.Context, c Context) ([]byte, error) {
	if m.Store == nil {
		return nil, ErrUnavailable
	}
	r, _, e := m.Store.Lookup(ctx, c)
	if e != nil {
		return nil, e
	}
	if r.State != "ready" {
		return nil, ErrUnknown
	}
	if r.Reference == nil {
		return nil, ErrNotFound
	}
	provider := m.Providers[r.Reference.Provider]
	if provider == nil {
		return nil, ErrUnavailable
	}
	secret, err := provider.Get(ctx, c, *r.Reference)
	if canceled := ctx.Err(); canceled != nil {
		Wipe(secret)
		return nil, canceled
	}
	if err != nil {
		Wipe(secret)
		return nil, err
	}
	if validateSecret(secret) != nil {
		Wipe(secret)
		return nil, ErrUnsafe
	}
	return secret, nil
}
