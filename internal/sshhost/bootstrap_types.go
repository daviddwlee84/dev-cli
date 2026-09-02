package sshhost

// BootstrapStatus is the stable aggregate result code for remote onboarding.
type BootstrapStatus string

const (
	BootstrapUnknown BootstrapStatus = "unknown"
	BootstrapReady   BootstrapStatus = "ready"
	BootstrapPartial BootstrapStatus = "partial"
)

// HopStatus is a stable per-hop bootstrap state.
type HopStatus string

const (
	HopPresent        HopStatus = "present"
	HopInstalled      HopStatus = "installed"
	HopWorkingSkipped HopStatus = "working_jump_skipped"
	HopManual         HopStatus = "manual_remediation"
	HopFailed         HopStatus = "failed"
	HopUnknown        HopStatus = "unknown"
	HopNotAttempted   HopStatus = "not_attempted"
)

// BootstrapRequest controls an effectful remote bootstrap. A Route may be
// supplied from ResolveRoute; otherwise Bootstrap resolves it. Key or Candidate
// must carry unexported material state produced by this Service.
type BootstrapRequest struct {
	Alias                           string             `json:"alias"`
	Route                           Route              `json:"route,omitempty"`
	Key                             KeyResult          `json:"key,omitempty"`
	Candidate                       KeyCandidate       `json:"candidate,omitempty"`
	TargetRemoteOS                  RemoteOS           `json:"target_remote_os,omitempty"`
	OSOverrides                     []RemoteOSOverride `json:"os_overrides,omitempty"`
	Interactive                     bool               `json:"interactive,omitempty"`
	InstallOnWorkingJump            bool               `json:"install_on_working_jump,omitempty"`
	AllowWindowsAdminAuthorizedKeys bool               `json:"allow_windows_admin_authorized_keys,omitempty"`
}

// BootstrapStep is a safe dry-run statement. State is unknown until an
// explicitly effectful catalog/route/probe/apply method runs.
type BootstrapStep struct {
	Code  string          `json:"code"`
	State BootstrapStatus `json:"state"`
}

// BootstrapPlan is a side-effect-free preview. It never invokes Runner or
// consults known_hosts/network/effective SSH config.
type BootstrapPlan struct {
	Alias       string          `json:"alias"`
	Status      BootstrapStatus `json:"status"`
	RouteKnown  bool            `json:"route_known"`
	Hops        []RouteHop      `json:"hops,omitempty"`
	Steps       []BootstrapStep `json:"steps"`
	Diagnostics []Diagnostic    `json:"diagnostics,omitempty"`
}

// HopResult exposes only safe booleans and stable codes. Present/Installed/
// Verified refer to the selected key; OrdinaryReady is the separate ordinary
// alias gate used by later fleet registration.
type HopResult struct {
	Alias          string     `json:"alias"`
	Reference      string     `json:"reference"`
	RemoteOS       RemoteOS   `json:"remote_os"`
	AdminState     AdminState `json:"admin_state"`
	Target         bool       `json:"target,omitempty"`
	Status         HopStatus  `json:"status"`
	Code           string     `json:"code,omitempty"`
	Present        bool       `json:"present"`
	Installed      bool       `json:"installed"`
	Verified       bool       `json:"verified"`
	OrdinaryBefore bool       `json:"ordinary_before"`
	OrdinaryReady  bool       `json:"ordinary_ready"`
	Skipped        bool       `json:"skipped,omitempty"`
	Unknown        bool       `json:"unknown,omitempty"`
	Ready          bool       `json:"ready"`
}

// BootstrapResult is resumable. Ready/FleetReady require a fresh ordinary
// target login after exact-key proof; Partial preserves all local/generated
// assets and never claims an uncertain installer made no change.
type BootstrapResult struct {
	Alias          string          `json:"alias"`
	Status         BootstrapStatus `json:"status"`
	Code           string          `json:"code"`
	Ready          bool            `json:"ready"`
	Partial        bool            `json:"partial"`
	FleetReady     bool            `json:"fleet_ready"`
	TargetRemoteOS RemoteOS        `json:"target_remote_os"`
	Hops           []HopResult     `json:"hops"`
}
