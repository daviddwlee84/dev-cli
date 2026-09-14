// Package sshvault creates new SSH keys in explicitly selected vaults. It never
// imports an existing private key, writes private material to disk, or manages
// an agent. Planning is explicit provider execution, not passive discovery.
package sshvault

import (
	"errors"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
)

type Provider string

const (
	Bitwarden     Provider = "bitwarden"
	OnePassword   Provider = "1password"
	PersonalVault          = "personal"
)

var (
	ErrUnsafe             = errors.New("invalid SSH vault key request")
	ErrUnavailable        = errors.New("SSH vault provider unavailable or returned invalid metadata")
	ErrLocked             = errors.New("SSH vault provider is locked or not authenticated; unlock it using its native tools")
	ErrUnsupportedVersion = errors.New("SSH vault provider version is not supported")
	ErrUnsupportedContext = errors.New("Bitwarden endpoint-attested creation is unsupported; native-profile mode requires separate approval and leaves endpoints unverified")
	ErrExperimental       = errors.New("Bitwarden in-memory SSH key generation requires explicit experimental approval")
	ErrNativeContext      = errors.New("Bitwarden native-profile execution context is unsupported, unsafe, or incomplete")
	ErrEntrypoint         = errors.New("Bitwarden entrypoint or Node runtime cannot be safely bound; unsupported wrappers and runtime shims are refused")
	ErrStale              = errors.New("SSH vault key plan or destination changed; review a new plan")
	ErrPlanUsed           = errors.New("SSH vault key creation was already attempted; do not retry without reconciling the vault")
	ErrUnknown            = errors.New("SSH vault mutation outcome or destination is unknown; inspect the vault before any new creation")
	ErrPublicKey          = errors.New("SSH vault item was created but its public key could not be verified; retain the item and reconcile it")
)

// Request selects a destination, not a saved password context. OnePassword
// requires an exact vault ID; AccountID may select an exact account or be empty
// to review the current account. Bitwarden requires both Experimental and
// NativeContextApproved: the latter explicitly delegates endpoint/configuration
// authority to the reviewed native profile, not to a base URL or a session key.
// Endpoint-attested Bitwarden creation remains unsupported. Its AccountID, when
// set, is an exact user ID. No unlock secret or private key belongs in this API.
type Request struct {
	Provider              Provider `json:"provider"`
	Title                 string   `json:"title"`
	AccountID             string   `json:"account_id,omitempty"`
	VaultID               string   `json:"vault_id,omitempty"`
	Experimental          bool     `json:"experimental,omitempty"`
	NativeContextApproved bool     `json:"native_context_approved,omitempty"`
}

// Destination is the reviewed provider identity. It does not authorize provider
// login, vault creation, organization selection, or native agent configuration.
// For Bitwarden, ServerURL is only a sanitized advisory base/display label;
// ProfilePath and the observed native user identify the delegated native context.
type Destination struct {
	Provider     Provider `json:"provider"`
	AccountID    string   `json:"account_id"`
	ServerURL    string   `json:"server_url"`
	UserID       string   `json:"user_id"`
	VaultID      string   `json:"vault_id"`
	VaultName    string   `json:"vault_name,omitempty"`
	AccountLabel string   `json:"account_label,omitempty"`
	ProfilePath  string   `json:"profile_path,omitempty"`
}

// NativeProfile describes the reviewed controller execution surface, not the
// native provider's internal endpoints or the complete dependency supply chain.
type NativeProfile struct {
	Path          string `json:"path"`
	RequestedPath string `json:"requested_path"`
	Entrypoint    string `json:"entrypoint"`
	Runtime       string `json:"runtime,omitempty"`
	PackageRoot   string `json:"package_root,omitempty"`
	CWD           string `json:"cwd"`
}

type Plan struct {
	Destination           Destination    `json:"destination"`
	Version               string         `json:"version"`
	Title                 string         `json:"title"`
	Experimental          bool           `json:"experimental,omitempty"`
	NativeContextApproved bool           `json:"native_context_approved,omitempty"`
	NativeProfile         *NativeProfile `json:"native_profile,omitempty"`
	EndpointStatus        string         `json:"endpoint_status,omitempty"`
	state                 *planState
}

type planState struct {
	service   *Service
	request   Request
	dest      Destination
	version   string
	operation string
	attempted bool
	native    *nativeContextProof
}

// Receipt contains only public material. Destination is the reviewed destination,
// not a claim that an external CLI could not change account while it ran. Consult
// Result.BindingStatus before relying on that association. An item ID may be
// known even when PublicLine and Fingerprint could not yet be obtained.
type Receipt struct {
	Destination
	ItemID      string `json:"item_id"`
	PublicLine  string `json:"public_line,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

const (
	StatusNotStarted         = "not_started"
	StatusCreated            = "created"
	StatusUnknown            = "unknown"
	BindingVerified          = "verified"
	BindingUnknown           = "unknown"
	NativeObservedConsistent = "observed_consistent"
	NativeUnknown            = "unknown"
	EndpointUnverified       = "unverified"
)

// Created means that a matching creation response was verified; it never means
// the selected SSH agent serves the key. ErrPublicKey and ErrUnknown can accompany
// a created result and its retained receipt. No automatic retry is permitted.
// Bitwarden native-profile mode always leaves BindingStatus unknown and endpoints
// unverified; observed_consistent describes only the reviewed native context.
type Result struct {
	Status              string   `json:"status"`
	BindingStatus       string   `json:"binding_status,omitempty"`
	NativeContextStatus string   `json:"native_context_status,omitempty"`
	EndpointStatus      string   `json:"endpoint_status,omitempty"`
	Receipt             *Receipt `json:"receipt,omitempty"`
	state               *resultState
}

type Service struct {
	runner   sshcredential.CommandRunner
	generate func(*planState) ([]byte, string, string, error)
	mu       sync.Mutex
}

func NewService(runner sshcredential.CommandRunner) *Service {
	if runner == nil {
		runner = sshcredential.NativeRunner{}
	}
	return &Service{runner: runner, generate: bitwardenInput}
}
