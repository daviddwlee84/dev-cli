package sshhost

// KeyType selects the native SSH key algorithm. Empty requests retain Ed25519.
type KeyType string

const (
	KeyTypeEd25519   KeyType = "ed25519"
	KeyTypeEd25519SK KeyType = "ed25519-sk"
	KeyTypeECDSASK   KeyType = "ecdsa-sk"
)

// SecurityKeyOptions configure an explicit native FIDO enrollment. They never
// authorize passive hardware enumeration or an alternative authentication method.
type SecurityKeyOptions struct {
	Provider       string `json:"provider,omitempty"`
	Resident       bool   `json:"resident,omitempty"`
	VerifyRequired bool   `json:"verify_required,omitempty"`
	Application    string `json:"application,omitempty"`
}

// SecurityKeyCapability is stat-only. Unknown permits a reviewed interactive
// attempt, not a claim that a token is present or that signing will succeed.
type SecurityKeyCapability struct {
	Status        string       `json:"status"`
	CanAttempt    bool         `json:"can_attempt"`
	Provider      string       `json:"provider"`
	KeygenPath    string       `json:"keygen_path,omitempty"`
	SSHClientPath string       `json:"ssh_client_path,omitempty"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

// HardwareKeyEffect is separate from local-file creation. A started enrollment
// may have changed a resident authenticator even if it returned no usable stub.
// Recovery paths refer only to independently validated SK stub/public pairs.
type HardwareKeyEffect struct {
	Kind                 string `json:"kind"`
	Status               string `json:"status"`
	Resident             bool   `json:"resident,omitempty"`
	RecoveryIdentityPath string `json:"recovery_identity_path,omitempty"`
	RecoveryPublicPath   string `json:"recovery_public_path,omitempty"`
}

// KeyLocalFileObservation reports a retained or uncertain path separately from
// a validated recovery pair. It never grants key or cleanup authority.
type KeyLocalFileObservation struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

func (t KeyType) securityKey() bool { return t == KeyTypeEd25519SK || t == KeyTypeECDSASK }

func (t KeyType) algorithm() string {
	switch t {
	case KeyTypeEd25519SK:
		return "sk-ssh-ed25519@openssh.com"
	case KeyTypeECDSASK:
		return "sk-ecdsa-sha2-nistp256@openssh.com"
	default:
		return "ssh-ed25519"
	}
}
