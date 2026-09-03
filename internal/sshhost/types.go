// Package sshhost owns static OpenSSH host discovery and the files in
// ~/.ssh/dev.d. OpenSSH remains the semantic authority: discovery never runs
// ssh or evaluates Match, while Effective obtains evaluated values from ssh -G.
package sshhost

import (
	"errors"
	"io/fs"
)

const (
	// ManagedFormatVersion is the on-disk dev-owned host fragment format.
	ManagedFormatVersion = 1
	// ManagedHeader identifies a file as a dev-owned v1 host fragment.
	ManagedHeader = "# dev-cli managed SSH host v1"
	// ManagedInclude is the only Include installed by Init.
	ManagedInclude = "~/.ssh/dev.d/*.conf"
)

var (
	ErrBlocked       = errors.New("SSH host operation is blocked")
	ErrInvalidAlias  = errors.New("invalid managed SSH alias")
	ErrNotManaged    = errors.New("SSH host file is not managed by dev")
	ErrSourceChanged = errors.New("SSH host source changed since planning")
	ErrUnsafePath    = errors.New("unsafe SSH path")
)

// Location identifies a source line. Paths are absolute and cleaned.
type Location struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// GuardKind is a static condition that guarded an Include edge.
type GuardKind string

const (
	GuardHost  GuardKind = "host"
	GuardMatch GuardKind = "match"
)

// Guard records the Host or Match context active at an Include directive.
// Arguments are lexical OpenSSH arguments with quotes removed; they are not
// executed or semantically expanded by dev.
type Guard struct {
	Kind      GuardKind `json:"kind"`
	Arguments []string  `json:"arguments,omitempty"`
	Source    Location  `json:"source"`
	Dynamic   bool      `json:"dynamic,omitempty"`
}

// IncludeFrame is one edge in a declaration's root-to-source provenance.
type IncludeFrame struct {
	Source   Location `json:"source"`
	Argument string   `json:"argument"`
	Resolved string   `json:"resolved"`
	Guards   []Guard  `json:"guards,omitempty"`
}

// Reachability is what static analysis can prove about an exact alias.
type Reachability string

const (
	Reachable   Reachability = "reachable"
	Unreachable Reachability = "unreachable"
	Unknown     Reachability = "unknown"
)

// Ownership describes the source of a discovered declaration.
type Ownership string

const (
	OwnershipForeign  Ownership = "foreign"
	OwnershipManaged  Ownership = "managed"
	OwnershipConflict Ownership = "conflict"
)

// AliasDefinition is one exact, positive alias named by a Host declaration.
type AliasDefinition struct {
	Alias        string         `json:"alias"`
	Patterns     []string       `json:"patterns"`
	Source       Location       `json:"source"`
	Provenance   []IncludeFrame `json:"provenance,omitempty"`
	Reachability Reachability   `json:"reachability"`
	Ownership    Ownership      `json:"ownership"`
}

// Alias is a selectable exact host and all statically discovered definitions.
type Alias struct {
	Name        string            `json:"name"`
	Definitions []AliasDefinition `json:"definitions"`
	Conflict    bool              `json:"conflict,omitempty"`
}

// HostDeclaration retains every Host line, including wildcard-only lines used
// for collision analysis. Exact aliases are the positive non-pattern tokens.
type HostDeclaration struct {
	Patterns     []string       `json:"patterns"`
	ExactAliases []string       `json:"exact_aliases,omitempty"`
	Source       Location       `json:"source"`
	Provenance   []IncludeFrame `json:"provenance,omitempty"`
}

// IncludeEdge records one expanded Include match, or one no-match/failed edge
// when Resolved is empty. Repeated non-cyclic edges are intentionally retained.
type IncludeEdge struct {
	Source   Location `json:"source"`
	Argument string   `json:"argument"`
	Resolved string   `json:"resolved,omitempty"`
	Guards   []Guard  `json:"guards,omitempty"`
	Cycle    bool     `json:"cycle,omitempty"`
	Repeated bool     `json:"repeated,omitempty"`
}

// ScannedFile is one visit in OpenSSH's inline lexical order. A path may appear
// more than once when the same file is included by repeated edges.
type ScannedFile struct {
	Path       string         `json:"path"`
	Visit      int            `json:"visit"`
	Bytes      int64          `json:"bytes"`
	Provenance []IncludeFrame `json:"provenance,omitempty"`
}

// Diagnostic is a stable, machine-readable conservative scanner or planner
// finding. Incomplete means discovery cannot prove the whole active closure;
// BlocksMutation means the finding prevents safe ownership changes.
type Diagnostic struct {
	Code           string    `json:"code"`
	Message        string    `json:"message"`
	Path           string    `json:"path,omitempty"`
	Source         *Location `json:"source,omitempty"`
	Incomplete     bool      `json:"incomplete,omitempty"`
	BlocksMutation bool      `json:"blocks_mutation,omitempty"`
}

// Inventory is the bounded static view rooted at ~/.ssh/config. Complete is
// false whenever a dynamic/unsupported construct or a bound prevented proof of
// the Include closure. No subprocess is involved in producing an Inventory.
type Inventory struct {
	Root                 string            `json:"root"`
	RootMissing          bool              `json:"root_missing,omitempty"`
	Complete             bool              `json:"complete"`
	ManagedIncludeActive bool              `json:"managed_include_active"`
	Files                []ScannedFile     `json:"files,omitempty"`
	Includes             []IncludeEdge     `json:"includes,omitempty"`
	Declarations         []HostDeclaration `json:"declarations,omitempty"`
	Aliases              []Alias           `json:"aliases,omitempty"`
	Diagnostics          []Diagnostic      `json:"diagnostics,omitempty"`
}

// Find returns the exact alias entry using OpenSSH's case-insensitive host-name
// comparison. The returned value is a copy.
func (i Inventory) Find(alias string) (Alias, bool) {
	for _, candidate := range i.Aliases {
		if equalAlias(candidate.Name, alias) {
			return candidate, true
		}
	}
	return Alias{}, false
}

// EffectiveConfig is the stable subset consumed by setup/bootstrap. Scalars
// are first-value-wins; IdentityFiles preserves ssh -G's additive order.
type EffectiveConfig struct {
	Alias          string              `json:"alias"`
	HostName       string              `json:"host_name,omitempty"`
	User           string              `json:"user,omitempty"`
	Port           int                 `json:"port,omitempty"`
	ProxyJump      string              `json:"proxy_jump,omitempty"`
	IdentityFiles  []string            `json:"identity_files,omitempty"`
	IdentitiesOnly *bool               `json:"identities_only,omitempty"`
	Values         map[string][]string `json:"-"`
}

// ManagedDefinition is the complete allowlisted v1 host fragment model.
type ManagedDefinition struct {
	Alias          string `json:"alias"`
	HostName       string `json:"host_name"`
	User           string `json:"user,omitempty"`
	Port           int    `json:"port,omitempty"`
	ProxyJump      string `json:"proxy_jump,omitempty"`
	IdentityFile   string `json:"identity_file,omitempty"`
	IdentitiesOnly *bool  `json:"identities_only,omitempty"`
}

// ManagedOperation selects upsert or removal planning.
type ManagedOperation string

const (
	ManagedUpsert ManagedOperation = "upsert"
	ManagedRemove ManagedOperation = "remove"
)

// ManagedRequest is input to PlanManaged. Definition is required for upsert;
// only Definition.Alias is consulted for removal.
type ManagedRequest struct {
	Operation  ManagedOperation  `json:"operation"`
	Definition ManagedDefinition `json:"definition"`
}

// PlanAction is a stable action/status code shared by plans and results.
type PlanAction string

const (
	ActionCreate  PlanAction = "create"
	ActionUpdate  PlanAction = "update"
	ActionRemove  PlanAction = "remove"
	ActionNoop    PlanAction = "noop"
	ActionBlocked PlanAction = "blocked"
)

// ManagedPlan is a renderable, source-bound plan. Its mutation bytes and source
// identity are private so ApplyManaged can reject fabricated or stale plans.
type ManagedPlan struct {
	Action       PlanAction         `json:"action"`
	Operation    ManagedOperation   `json:"operation"`
	Alias        string             `json:"alias"`
	Path         string             `json:"path"`
	BeforeDigest string             `json:"before_digest,omitempty"`
	AfterDigest  string             `json:"after_digest,omitempty"`
	Mode         fs.FileMode        `json:"mode,omitempty"`
	Definition   *ManagedDefinition `json:"definition,omitempty"`
	Diagnostics  []Diagnostic       `json:"diagnostics,omitempty"`
	state        *managedPlanState
}

// Ready reports whether a plan may be applied (including an idempotent no-op).
func (p ManagedPlan) Ready() bool { return p.Action != ActionBlocked && p.state != nil }

// ManagedResult reports the converged local fragment state.
type ManagedResult struct {
	Action     PlanAction `json:"action"`
	Alias      string     `json:"alias"`
	Path       string     `json:"path"`
	Changed    bool       `json:"changed"`
	Verified   bool       `json:"verified,omitempty"`
	RolledBack bool       `json:"rolled_back,omitempty"`
	Digest     string     `json:"digest,omitempty"`
}

// InitPlan is a read-only proposal to install ManagedInclude in the root user
// config. The desired bytes and metadata snapshot are private.
type InitPlan struct {
	Action       PlanAction   `json:"action"`
	Path         string       `json:"path"`
	ManagedDir   string       `json:"managed_dir"`
	Include      string       `json:"include"`
	BeforeDigest string       `json:"before_digest,omitempty"`
	AfterDigest  string       `json:"after_digest,omitempty"`
	InsertOffset int          `json:"insert_offset,omitempty"`
	Mode         fs.FileMode  `json:"mode,omitempty"`
	BOM          bool         `json:"bom,omitempty"`
	Newline      string       `json:"newline,omitempty"`
	Diagnostics  []Diagnostic `json:"diagnostics,omitempty"`
	state        *initPlanState
}

// Ready reports whether an init plan may be applied (including a no-op).
func (p InitPlan) Ready() bool { return p.Action != ActionBlocked && p.state != nil }

// InitResult reports whether the root Include or secure directory was created.
type InitResult struct {
	Action  PlanAction `json:"action"`
	Path    string     `json:"path"`
	Changed bool       `json:"changed"`
	Digest  string     `json:"digest,omitempty"`
}
