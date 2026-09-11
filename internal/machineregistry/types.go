// Package machineregistry owns controller-local machine identities and explicit
// associations with provider records. Registry IDs are not remote dev UUID pins;
// this package never edits SSH, fleet, Herdr, or Tailscale configuration.
package machineregistry

import (
	"errors"
	"io/fs"
)

const SchemaVersion = 2

var (
	ErrStale       = errors.New("machine registry changed since planning")
	ErrConflict    = errors.New("machine registry association conflicts")
	ErrNotFound    = errors.New("machine registry record not found")
	ErrSchema      = errors.New("unsupported machine registry schema")
	ErrUnsafePath  = errors.New("unsafe machine registry path or permissions")
	ErrInvalidPlan = errors.New("invalid machine registry plan")
)

type Machine struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	Revision         uint64 `json:"revision"`
	MergedInto       string `json:"merged_into,omitempty"`
	PreferredProfile string `json:"preferred_profile,omitempty"`
}

// Binding identifies a provider record in its original namespace. Fingerprint
// is the reviewed source/provenance fingerprint, not proof of remote identity.
// An unlinked record is retained with Suppressed=true and no MachineID, so a
// future discovery cannot silently undo the user's explicit unlink.
type Binding struct {
	Provider    string `json:"provider"`
	Scope       string `json:"scope"`
	NativeID    string `json:"native_id"`
	Fingerprint string `json:"fingerprint,omitempty"`
	MachineID   string `json:"machine_id,omitempty"`
	Suppressed  bool   `json:"suppressed,omitempty"`
}

type Snapshot struct {
	SchemaVersion int         `json:"schema_version"`
	Revision      uint64      `json:"revision"`
	Machines      []Machine   `json:"machines"`
	Bindings      []Binding   `json:"bindings"`
	Imports       []SSHImport `json:"ssh_imports,omitempty"`
}

// SSHImport retains explicit source-to-local profile intent; OpenSSH owns the
// actual connection settings and observations never authorize machine merging.
type SSHImport struct {
	LocalAlias            string `json:"local_alias"`
	OriginID              string `json:"origin_id"`
	ProfileID             string `json:"profile_id"`
	FleetHost             string `json:"fleet_host"`
	RemoteAlias           string `json:"remote_alias"`
	SourceFingerprint     string `json:"source_fingerprint"`
	RouteFingerprint      string `json:"route_fingerprint"`
	DefinitionFingerprint string `json:"definition_fingerprint"`
}

type Request struct {
	Action    string      `json:"action"` // adopt, link, unlink, merge
	MachineID string      `json:"machine_id,omitempty"`
	Into      string      `json:"into,omitempty"`
	Label     string      `json:"label,omitempty"`
	Bindings  []Binding   `json:"bindings,omitempty"`
	Imports   []SSHImport `json:"ssh_imports,omitempty"`
}

// Plan is an in-process guarded plan. Its public preview cannot be modified to
// change Apply's authority, and a serialized/deserialized plan cannot be applied.
type Plan struct {
	SchemaVersion int      `json:"schema_version"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	MachineID     string   `json:"machine_id"`
	Into          string   `json:"into,omitempty"`
	Revision      uint64   `json:"revision"`
	Effects       []string `json:"effects"`
	After         Snapshot `json:"after"`
	state         *planState
}

type Result struct {
	SchemaVersion int      `json:"schema_version"`
	Status        string   `json:"status"`
	MachineID     string   `json:"machine_id"`
	Into          string   `json:"into,omitempty"`
	Snapshot      Snapshot `json:"snapshot"`
}

type sourceState struct {
	database fs.FileInfo
	anchor   string
	identity fs.FileInfo
}

type planState struct {
	path    string
	request Request
	before  Snapshot
	after   Snapshot
	source  sourceState
	changed bool
}

func emptySnapshot() Snapshot {
	return Snapshot{SchemaVersion: SchemaVersion, Machines: []Machine{}, Bindings: []Binding{}}
}

func cloneSnapshot(s Snapshot) Snapshot {
	s.Machines = append([]Machine{}, s.Machines...)
	s.Bindings = append([]Binding{}, s.Bindings...)
	s.Imports = append([]SSHImport(nil), s.Imports...)
	return s
}

func (s Snapshot) LookupImport(alias string) (SSHImport, bool) {
	for _, imported := range s.Imports {
		if imported.LocalAlias == alias {
			return imported, true
		}
	}
	return SSHImport{}, false
}

// Find resolves retained merge redirects and returns the active machine.
func (s Snapshot) Find(id string) (Machine, bool) {
	for remaining := len(s.Machines); remaining > 0; remaining-- {
		found := false
		for _, machine := range s.Machines {
			if machine.ID != id {
				continue
			}
			if machine.MergedInto == "" {
				return machine, true
			}
			id, found = machine.MergedInto, true
			break
		}
		if !found {
			break
		}
	}
	return Machine{}, false
}

// LookupBinding finds an exact provider-scoped association, including an
// explicitly suppressed association. Callers must compare Fingerprint with fresh
// provider observations before treating the binding as current.
func (s Snapshot) LookupBinding(provider, scope, nativeID string) (Binding, bool) {
	for _, binding := range s.Bindings {
		if binding.Provider == provider && binding.Scope == scope && binding.NativeID == nativeID {
			return binding, true
		}
	}
	return Binding{}, false
}
