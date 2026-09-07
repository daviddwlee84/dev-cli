// Package agentinterop plans explicit agent configuration transfers. Inventory
// remains in agentmcp/agentskill; neither inventory nor planning launches tools.
package agentinterop

import (
	"errors"
	"io/fs"
)

const SchemaVersion = 1

var (
	ErrStale       = errors.New("stale transfer plan; create a new plan")
	ErrConflict    = errors.New("destination conflicts with the selected transfer")
	ErrUnsupported = errors.New("unsupported transfer capability")
	ErrCredentials = errors.New("needs-local-configuration: use credential references")
)

// ScopeRef identifies one exact checkout or explicitly selected user directory.
type ScopeRef struct {
	Scope string `json:"scope" toml:"scope"`
	Root  string `json:"root" toml:"root"`
}

type ArtifactRef struct {
	ScopeRef
	Agent string `json:"agent,omitempty" toml:"agent,omitempty"`
	Path  string `json:"path,omitempty" toml:"path,omitempty"`
}

// SecretRef contains names, never resolved values. File is an explicitly
// selected local JSON env source and is not exported in portable recipes.
type SecretRef struct {
	Name     string `json:"name" toml:"name"`
	Variable string `json:"variable" toml:"variable"`
}

type TransferRequest struct {
	Kind       string      `json:"kind" toml:"kind"`
	Mode       string      `json:"mode" toml:"mode"`
	From       ArtifactRef `json:"from" toml:"from"`
	To         ArtifactRef `json:"to" toml:"to"`
	Name       string      `json:"name,omitempty" toml:"name,omitempty"`
	TargetName string      `json:"target_name,omitempty" toml:"target_name,omitempty"`
	Style      string      `json:"style,omitempty" toml:"style,omitempty"`
	Transport  string      `json:"transport,omitempty" toml:"transport,omitempty"`
	Bindings   []SecretRef `json:"bindings,omitempty" toml:"bindings,omitempty"`
	EnvFile    string      `json:"env_file,omitempty" toml:"-"`
	Launcher   string      `json:"launcher,omitempty" toml:"-"`
	Prepared   string      `json:"prepared,omitempty" toml:"-"`
	Adopt      bool        `json:"adopt,omitempty" toml:"adopt,omitempty"`
}

type Location struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

type Change struct {
	Location
	Action string `json:"action"`
}

// Plan is the public, sanitized view. Payloads and authority checks are private
// and cannot be supplied by editing this report or a portable recipe.
type Plan struct {
	SchemaVersion int      `json:"schema_version"`
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Mode          string   `json:"mode"`
	Status        string   `json:"status"`
	Changes       []Change `json:"changes"`
	Notes         []string `json:"notes"`
}

type ApplyResult struct {
	Plan
	Completed int  `json:"completed"`
	Total     int  `json:"total"`
	Uncertain bool `json:"uncertain,omitempty"`
}

// image holds no raw file bytes. Blob references point into an owner-private
// recovery store. Digests are keyed and never exposed in public output.
type image struct {
	Kind     string      `json:"kind"`
	Mode     fs.FileMode `json:"mode,omitempty"`
	Identity string      `json:"identity,omitempty"`
	Digest   string      `json:"digest,omitempty"`
	Blob     string      `json:"blob,omitempty"`
	Link     string      `json:"link,omitempty"`
}

type observation struct {
	Location
	Image image `json:"image"`
}

type effect struct {
	Location
	Before image  `json:"before"`
	After  image  `json:"after"`
	Post   *image `json:"post,omitempty"`
}

type payload struct {
	Path  string `json:"path"`
	Image image  `json:"image"`
}

type record struct {
	Schema       int               `json:"schema"`
	ID           string            `json:"id"`
	Request      TransferRequest   `json:"request"`
	Status       string            `json:"status"`
	Roots        map[string]string `json:"roots"`
	Guards       []observation     `json:"guards"`
	Effects      []effect          `json:"effects"`
	Completed    int               `json:"completed"`
	InFlight     *int              `json:"in_flight,omitempty"`
	Notes        []string          `json:"notes"`
	Parent       string            `json:"parent,omitempty"`
	MAC          string            `json:"mac"`
	Payload      []payload         `json:"payload,omitempty"`
	ProviderLock *image            `json:"provider_lock,omitempty"`
}

type Service struct{ StateDir string }

func (r record) public() Plan {
	p := Plan{SchemaVersion: SchemaVersion, ID: r.ID, Kind: r.Request.Kind,
		Mode: r.Request.Mode, Status: r.Status, Changes: []Change{}, Notes: append([]string{}, r.Notes...)}
	for _, e := range r.Effects {
		action := "replace"
		if e.Before.Kind == "absent" {
			action = "create"
		}
		if e.After.Kind == "absent" {
			action = "remove"
		}
		if e.After.Kind == "link" {
			action = "link"
		}
		p.Changes = append(p.Changes, Change{Location: e.Location, Action: action})
	}
	return p
}

func (r record) result() ApplyResult {
	return ApplyResult{Plan: r.public(), Completed: r.Completed, Total: len(r.Effects), Uncertain: r.InFlight != nil}
}
