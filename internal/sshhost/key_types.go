package sshhost

import "errors"

const (
	// MaxPublicKeyLineBytes bounds every public-key record accepted from disk,
	// ssh-add, ssh-keygen, or a caller. It includes an optional line ending.
	MaxPublicKeyLineBytes = 16 << 10
	// MaxPublicKeyBlobBytes bounds the decoded SSH wire blob fingerprinted by dev.
	MaxPublicKeyBlobBytes = 8 << 10
)

var (
	// ErrInteractionRequired means native credential interaction is required but
	// the operation was requested in batch mode.
	ErrInteractionRequired = errors.New("SSH interaction required")
	// ErrKeyCollision means a key destination appeared or already exists. Key
	// publication is always no-replace.
	ErrKeyCollision = errors.New("SSH key destination already exists")
	// ErrUnsupportedRoute means ProxyJump syntax or policy cannot be represented
	// conservatively by the bootstrapper.
	ErrUnsupportedRoute = errors.New("unsupported SSH route")
	// ErrManualRemediation means policy was preserved and a user must complete or
	// inspect a step rather than dev weakening SSH or filesystem protections.
	ErrManualRemediation = errors.New("manual SSH remediation required")
)

// KeyMetadata is the content-free result of parsing one OpenSSH public record.
type KeyMetadata struct {
	Algorithm   string `json:"algorithm"`
	Comment     string `json:"comment,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

// KeySource identifies where a candidate was learned without exposing its
// public record or agent payload.
type KeySource string

const (
	KeySourceEffectiveIdentity KeySource = "effective_identity"
	KeySourcePublicFile        KeySource = "public_file"
	KeySourceAgent             KeySource = "agent"
	KeySourceExplicit          KeySource = "explicit"
	KeySourceGenerated         KeySource = "generated"
	KeySourceDerived           KeySource = "derived"
)

// KeyProvenance records usable local mechanisms without reading or returning
// private-key content.
type KeyProvenance struct {
	Effective       bool `json:"effective,omitempty"`
	Private         bool `json:"private,omitempty"`
	SecurityKeyStub bool `json:"security_key_stub,omitempty"`
	Agent           bool `json:"agent,omitempty"`
}

// KeyCandidate is safe to marshal. The normalized public line needed by a
// later installer is deliberately held only in unexported service state.
type KeyCandidate struct {
	Source       KeySource     `json:"source"`
	Sources      []KeySource   `json:"sources,omitempty"`
	Algorithm    string        `json:"algorithm"`
	Comment      string        `json:"comment,omitempty"`
	Fingerprint  string        `json:"fingerprint"`
	PublicPath   string        `json:"public_path,omitempty"`
	IdentityFile string        `json:"identity_file,omitempty"`
	Provenance   KeyProvenance `json:"provenance"`
	state        *keyMaterialState
}

// KeyCatalog is an effectful snapshot of validated local public candidates.
// Complete is false when a bounded source could not be fully inspected.
type KeyCatalog struct {
	Candidates  []KeyCandidate `json:"candidates,omitempty"`
	Complete    bool           `json:"complete"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
}

// KeyCatalogRequest controls an explicitly effectful catalog operation. When
// Effective is nil, Alias is evaluated with plain ssh -G. Agent enumeration is
// enabled unless NoAgent is true.
type KeyCatalogRequest struct {
	Alias     string           `json:"alias,omitempty"`
	Effective *EffectiveConfig `json:"-"`
	NoAgent   bool             `json:"no_agent,omitempty"`
}

// KeyOperation selects an existing key, derives a missing public companion, or
// generates a new Ed25519 pair.
type KeyOperation string

const (
	KeyUse      KeyOperation = "use"
	KeyDerive   KeyOperation = "derive"
	KeyGenerate KeyOperation = "generate"
)

// KeyRequest is policy input to PlanKey. AllowDerive represents confirmation
// already obtained by a CLI; it is never a credential. Noninteractive
// generation additionally requires NoPassphrase.
type KeyRequest struct {
	Operation           KeyOperation `json:"operation,omitempty"`
	Candidate           KeyCandidate `json:"candidate,omitempty"`
	Path                string       `json:"path,omitempty"`
	DestinationIdentity string       `json:"destination_identity,omitempty"`
	Comment             string       `json:"comment,omitempty"`
	Interactive         bool         `json:"interactive,omitempty"`
	AllowDerive         bool         `json:"allow_derive,omitempty"`
	NoPassphrase        bool         `json:"no_passphrase,omitempty"`
}

// KeyPlan is content-free and source-bound. ApplyKey accepts only a ready plan
// produced by the same Service; public fields cannot forge execution state.
type KeyPlan struct {
	Action       PlanAction   `json:"action"`
	Operation    KeyOperation `json:"operation"`
	Source       KeySource    `json:"source,omitempty"`
	Algorithm    string       `json:"algorithm,omitempty"`
	Comment      string       `json:"comment,omitempty"`
	Fingerprint  string       `json:"fingerprint,omitempty"`
	PublicPath   string       `json:"public_path,omitempty"`
	IdentityFile string       `json:"identity_file,omitempty"`
	Diagnostics  []Diagnostic `json:"diagnostics,omitempty"`
	state        *keyPlanState
}

// Ready reports whether ApplyKey may consume this plan.
func (p KeyPlan) Ready() bool { return p.Action != ActionBlocked && p.state != nil }

// KeyResult reports a selected or created key without exposing its public line.
// Retained is true for generated assets, which bootstrap never rolls back.
type KeyResult struct {
	Action    PlanAction   `json:"action"`
	Operation KeyOperation `json:"operation"`
	Candidate KeyCandidate `json:"candidate"`
	Created   bool         `json:"created,omitempty"`
	Retained  bool         `json:"retained,omitempty"`
}
