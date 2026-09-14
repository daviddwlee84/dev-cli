package sshflow

import (
	"fmt"
	"sync/atomic"

	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

// OnboardRequest carries an exact discovery observation into a reviewed setup.
// Reports are provenance, never authentication or machine identity authority.
type OnboardRequest struct {
	Candidate             *sshdiscovery.Candidate
	Report                *sshdiscovery.Report
	MachineID             string
	Alias, HostName, User string
	Port                  int
	Auth                  string
	KeyPath               string
	KeyComment            string
	KeyType               sshhost.KeyType
	SecurityKey           sshhost.SecurityKeyOptions
	VaultReceipts         []VaultKeyReceipt
	// KeyFingerprint and KeyAgentSocket select a key held by a named agent.
	KeyFingerprint, KeyAgentSocket                    string
	GenerateKey                                       bool
	To, RemoteOS, FleetName, HerdrLabel, HerdrSession string
	// Profile installs a key for this existing alias; its connection fields
	// are read-only and never rewritten from the request.
	Profile *ConnectionProfile
}

// OnboardPreview is the display projection of an opaque service-bound plan.
type OnboardPreview struct {
	Targets         []OnboardTarget
	Init            sshhost.InitPlan
	Notes           []string
	PreserveAliases []string
}

// OnboardExecutionResult retains confirmed effects even when a later stage
// fails or is interrupted. It is shared by terminal and dashboard renderers.
type OnboardExecutionResult struct {
	OnboardResult
	Init           *sshhost.InitResult                `json:"init,omitempty"`
	Configurations map[string]sshhost.ManagedResult   `json:"configurations,omitempty"`
	Keys           map[string]sshhost.KeyResult       `json:"keys,omitempty"`
	Bootstraps     map[string]sshhost.BootstrapResult `json:"bootstraps,omitempty"`
	Registrations  map[string]Result                  `json:"registrations,omitempty"`
	Bindings       map[string]machineregistry.Result  `json:"bindings,omitempty"`
	VaultReceipts  []VaultKeyReceipt                  `json:"vault_receipts,omitempty"`
}

// VaultKeyRequest is public intent. It contains no private key, unlock secret,
// provider session or native environment. Desktop handoff does not create an item.
type VaultKeyRequest struct {
	Provider              string `json:"provider"`
	AccountID             string `json:"account_id,omitempty"`
	VaultID               string `json:"vault_id,omitempty"`
	Title                 string `json:"title"`
	Experimental          bool   `json:"experimental,omitempty"`
	NativeContextApproved bool   `json:"native_context_approved,omitempty"`
	Desktop               bool   `json:"desktop,omitempty"`
}

// VaultKeyPreview is a display projection, never apply authority. IntentOnly
// means account, vault, tools and agent have not been observed by a service plan.
type VaultKeyPreview struct {
	AttemptID         string          `json:"attempt_id,omitempty"`
	Request           VaultKeyRequest `json:"request"`
	IntentOnly        bool            `json:"intent_only,omitempty"`
	AccountID         string          `json:"account_id,omitempty"`
	UserID            string          `json:"user_id,omitempty"`
	VaultID           string          `json:"vault_id,omitempty"`
	VaultName         string          `json:"vault_name,omitempty"`
	AccountLabel      string          `json:"account_label,omitempty"`
	ServerLabel       string          `json:"server_label,omitempty"`
	Version           string          `json:"version,omitempty"`
	ToolPath          string          `json:"tool_path,omitempty"`
	NativeProfilePath string          `json:"native_profile_path,omitempty"`
	NativeEntrypoint  string          `json:"native_entrypoint,omitempty"`
	NativeRuntime     string          `json:"native_runtime,omitempty"`
	NativePackageRoot string          `json:"native_package_root,omitempty"`
	NativeCWD         string          `json:"native_cwd,omitempty"`
	EndpointStatus    string          `json:"endpoint_status,omitempty"`
	Notes             []string        `json:"notes,omitempty"`
}

// VaultKeyReceipt reports an attempt and any observed item identity, not a
// signing proof. Empty ItemID with Status unknown must not imply an item exists.
// Account/vault fields remain reviewed intent when BindingStatus is unknown.
type VaultKeyReceipt struct {
	AttemptID             string             `json:"attempt_id,omitempty"`
	Observation           uint64             `json:"observation,omitempty"`
	ConflictingIdentities []VaultKeyIdentity `json:"conflicting_identities,omitempty"`
	Provider              string             `json:"provider"`
	Title                 string             `json:"title"`
	AccountID             string             `json:"account_id,omitempty"`
	VaultID               string             `json:"vault_id,omitempty"`
	ItemID                string             `json:"item_id,omitempty"`
	Fingerprint           string             `json:"fingerprint,omitempty"`
	Status                string             `json:"status"`
	BindingStatus         string             `json:"binding_status,omitempty"`
	NativeContextStatus   string             `json:"native_context_status,omitempty"`
	EndpointStatus        string             `json:"endpoint_status,omitempty"`
	ProfilePath           string             `json:"profile_path,omitempty"`
}

// VaultKeyIdentity retains conflicting nonempty observations for reconciliation;
// neither the primary receipt nor an alternative grants creation/key authority.
type VaultKeyIdentity struct {
	Provider    string `json:"provider,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	VaultID     string `json:"vault_id,omitempty"`
	ItemID      string `json:"item_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

var vaultObservationSequence atomic.Uint64

// NextVaultObservation numbers controller observations, not provider versions or
// credentials. Receipt ordering is independent of asynchronous delivery ordering.
func NextVaultObservation() uint64 { return vaultObservationSequence.Add(1) }

// VaultAgentKey is public selection metadata. No complete public line is exposed
// by creation projections, and agent advertisement is not authentication proof.
type VaultAgentKey struct {
	Provider    string `json:"provider"`
	Socket      string `json:"socket"`
	Fingerprint string `json:"fingerprint"`
	Algorithm   string `json:"algorithm"`
	Comment     string `json:"comment,omitempty"`
}

type VaultKeyResult struct {
	AttemptID           string           `json:"attempt_id,omitempty"`
	Status              string           `json:"status"`
	BindingStatus       string           `json:"binding_status,omitempty"`
	NativeContextStatus string           `json:"native_context_status,omitempty"`
	EndpointStatus      string           `json:"endpoint_status,omitempty"`
	Context             VaultKeyPreview  `json:"context"`
	Receipt             *VaultKeyReceipt `json:"receipt,omitempty"`
	AgentStatus         string           `json:"agent_status"`
	Keys                []VaultAgentKey  `json:"keys,omitempty"`
	Notes               []string         `json:"notes,omitempty"`
}

// Lines renders only the public projection shared by terminal and dashboard.
func (p VaultKeyPreview) Lines() []string {
	lines := []string{"Provider: " + p.Request.Provider, "Title: " + p.Request.Title}
	if p.AttemptID != "" {
		lines = append(lines, "Controller attempt ID: "+p.AttemptID)
	}
	if p.IntentOnly {
		lines = append(lines, "Intent-only preview: account, vault, native context and agent are UNOBSERVED. This is not an apply-ready service plan.", "Requested account ID: "+p.Request.AccountID, "Requested vault ID: "+p.Request.VaultID)
	} else if !p.Request.Desktop {
		lines = append(lines, "Reviewed account ID: "+p.AccountID, "Reviewed user ID: "+p.UserID, "Reviewed vault: "+p.VaultID+" "+p.VaultName, "Advisory server label: "+p.ServerLabel, "CLI path observed: "+p.ToolPath, "CLI version: "+p.Version)
	}
	if p.NativeProfilePath != "" {
		lines = append(lines, "Native profile: "+p.NativeProfilePath, "Native entrypoint: "+p.NativeEntrypoint, "Native runtime: "+p.NativeRuntime, "Native package root: "+p.NativePackageRoot, "Native working directory: "+p.NativeCWD)
	}
	if p.Request.Provider == "bitwarden" && !p.Request.Desktop {
		lines = append(lines, fmt.Sprintf("Experimental approval: %t; native-context delegation approval: %t", p.Request.Experimental, p.Request.NativeContextApproved))
	}
	if p.EndpointStatus != "" {
		lines = append(lines, "Endpoint status: "+p.EndpointStatus)
	}
	return append(lines, p.Notes...)
}

func (r VaultKeyReceipt) Lines() []string {
	lines := []string{fmt.Sprintf("Vault item/attempt retained: %s · %s · %s", r.Provider, r.Title, r.Status), "Reviewed account/vault: " + r.AccountID + " / " + r.VaultID}
	if r.AttemptID != "" {
		lines = append(lines, "Controller attempt ID: "+r.AttemptID)
	}
	if r.ItemID != "" {
		lines = append(lines, "Item ID: "+r.ItemID)
	} else if len(r.ConflictingIdentities) > 0 {
		lines = append(lines, "No unambiguous primary item ID; conflicting observations are retained below.")
	} else {
		lines = append(lines, "No item ID was observed; inspect the native vault before any new creation attempt.")
	}
	if r.Fingerprint != "" {
		lines = append(lines, "Fingerprint: "+r.Fingerprint)
	}
	if r.BindingStatus != "" {
		lines = append(lines, "Destination binding: "+r.BindingStatus)
	}
	if r.NativeContextStatus != "" {
		lines = append(lines, "Native context: "+r.NativeContextStatus)
	}
	if r.EndpointStatus != "" {
		lines = append(lines, "Endpoints: "+r.EndpointStatus)
	}
	if r.ProfilePath != "" {
		lines = append(lines, "Native profile: "+r.ProfilePath)
	}
	for _, conflict := range r.ConflictingIdentities {
		lines = append(lines, fmt.Sprintf("Conflicting identity observed for this attempt: %s account=%s vault=%s item=%s fingerprint=%s; reconcile the native vault.", conflict.Provider, conflict.AccountID, conflict.VaultID, conflict.ItemID, conflict.Fingerprint))
	}
	return lines
}

func (r VaultKeyResult) Lines() []string {
	lines := []string{"Vault action: " + r.Status, "Agent observation: " + r.AgentStatus}
	if r.Receipt != nil {
		lines = append(lines, r.Receipt.Lines()...)
	} else {
		lines = append(lines, "No vault item receipt was observed.")
	}
	return append(lines, r.Notes...)
}

func CloneVaultKeyReceipt(receipt VaultKeyReceipt) VaultKeyReceipt {
	receipt.ConflictingIdentities = append([]VaultKeyIdentity(nil), receipt.ConflictingIdentities...)
	return receipt
}

func CloneVaultKeyReceipts(receipts []VaultKeyReceipt) []VaultKeyReceipt {
	if receipts == nil {
		return nil
	}
	result := make([]VaultKeyReceipt, len(receipts))
	for i, receipt := range receipts {
		result[i] = CloneVaultKeyReceipt(receipt)
	}
	return result
}

// RetainVaultKeyReceipt is a display/reconciliation ledger, never key authority.
// IDs/created existence are monotonic; context observations follow controller
// ordering, so delayed consistent replies cannot erase later uncertainty.
func RetainVaultKeyReceipt(receipts []VaultKeyReceipt, receipt *VaultKeyReceipt) []VaultKeyReceipt {
	result := CloneVaultKeyReceipts(receipts)
	if receipt == nil {
		return result
	}
	if receipt.AttemptID != "" {
		for i, existing := range result {
			if existing.AttemptID == receipt.AttemptID {
				result[i] = mergeVaultKeyReceipt(existing, *receipt)
				return result
			}
		}
	}
	// Missing controller identity never authorizes structural deduplication.
	return append(result, CloneVaultKeyReceipt(*receipt))
}

func vaultReceiptIdentity(r VaultKeyReceipt) VaultKeyIdentity {
	return VaultKeyIdentity{Provider: r.Provider, AccountID: r.AccountID, VaultID: r.VaultID, ItemID: r.ItemID, Fingerprint: r.Fingerprint}
}

func vaultIdentitiesConflict(a, b VaultKeyIdentity) bool {
	for _, pair := range [][2]string{{a.Provider, b.Provider}, {a.AccountID, b.AccountID}, {a.VaultID, b.VaultID}, {a.ItemID, b.ItemID}, {a.Fingerprint, b.Fingerprint}} {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			return true
		}
	}
	return false
}

func mergeVaultKeyReceipt(existing, incoming VaultKeyReceipt) VaultKeyReceipt {
	conflict := vaultIdentitiesConflict(vaultReceiptIdentity(existing), vaultReceiptIdentity(incoming))
	addConflict := func(value VaultKeyIdentity) {
		for _, known := range existing.ConflictingIdentities {
			if known == value {
				return
			}
		}
		existing.ConflictingIdentities = append(existing.ConflictingIdentities, value)
	}
	if conflict {
		addConflict(vaultReceiptIdentity(incoming))
	}
	for _, value := range incoming.ConflictingIdentities {
		addConflict(value)
	}
	fill := func(target *string, value string) {
		if *target == "" {
			*target = value
		}
	}
	if !conflict {
		fill(&existing.Provider, incoming.Provider)
		fill(&existing.AccountID, incoming.AccountID)
		fill(&existing.VaultID, incoming.VaultID)
		fill(&existing.ItemID, incoming.ItemID)
		fill(&existing.Fingerprint, incoming.Fingerprint)
	}
	fill(&existing.Title, incoming.Title)
	fill(&existing.ProfilePath, incoming.ProfilePath)
	rank := func(status string) int {
		switch status {
		case "created":
			return 2
		case "unknown":
			return 1
		}
		return 0
	}
	if rank(incoming.Status) > rank(existing.Status) {
		existing.Status = incoming.Status
	}
	if incoming.Observation > existing.Observation {
		if incoming.BindingStatus != "" {
			existing.BindingStatus = incoming.BindingStatus
		}
		if incoming.NativeContextStatus != "" {
			existing.NativeContextStatus = incoming.NativeContextStatus
		}
		if incoming.EndpointStatus != "" {
			existing.EndpointStatus = incoming.EndpointStatus
		}
		existing.Observation = incoming.Observation
	} else if incoming.Observation == existing.Observation {
		fill(&existing.BindingStatus, incoming.BindingStatus)
		fill(&existing.NativeContextStatus, incoming.NativeContextStatus)
		fill(&existing.EndpointStatus, incoming.EndpointStatus)
		if incoming.BindingStatus == "unknown" {
			existing.BindingStatus = "unknown"
		}
		if incoming.NativeContextStatus == "unknown" {
			existing.NativeContextStatus = "unknown"
		}
		if incoming.EndpointStatus == "unverified" {
			existing.EndpointStatus = "unverified"
		}
	}
	if len(existing.ConflictingIdentities) > 0 {
		existing.BindingStatus, existing.NativeContextStatus = "unknown", "unknown"
	}
	return existing
}
