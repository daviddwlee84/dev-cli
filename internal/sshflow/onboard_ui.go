package sshflow

import (
	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

// OnboardRequest carries an exact discovery observation into a reviewed setup.
// Reports are provenance, never authentication or machine identity authority.
type OnboardRequest struct {
	Candidate                                         *sshdiscovery.Candidate
	Report                                            *sshdiscovery.Report
	MachineID                                         string
	Alias, HostName, User                             string
	Port                                              int
	Auth                                              string
	KeyPath                                           string
	GenerateKey                                       bool
	To, RemoteOS, FleetName, HerdrLabel, HerdrSession string
	// Profile installs a key for this existing alias; its connection fields
	// are read-only and never rewritten from the request.
	Profile *ConnectionProfile
}

// OnboardPreview is the display projection of an opaque service-bound plan.
type OnboardPreview struct {
	Targets []OnboardTarget
	Init    sshhost.InitPlan
	Notes   []string
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
}
