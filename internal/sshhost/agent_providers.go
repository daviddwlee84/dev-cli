package sshhost

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/platformfs"
)

// AgentProviderID names an SSH agent application whose socket dev can locate
// without executing it.
type AgentProviderID string

const (
	AgentProviderBitwarden AgentProviderID = "bitwarden"
	AgentProvider1Password AgentProviderID = "1password"
	AgentProviderSecretive AgentProviderID = "secretive"
	AgentProviderCustom    AgentProviderID = "custom"
	// AgentProviderWindowsPipe is the shared Windows OpenSSH agent pipe. Any
	// vendor agent may own it, so it is never attributed to one.
	AgentProviderWindowsPipe AgentProviderID = "windows-openssh-pipe"
)

const windowsOpenSSHAgentPipe = `\\.\pipe\openssh-ssh-agent`

// ErrAgentProviderUnavailable means a named agent's socket was not found or
// failed the socket safety checks.
var ErrAgentProviderUnavailable = errors.New("SSH agent provider socket is unavailable")

// AgentSocketRef selects one explicit agent socket. It is a path, never key
// material.
type AgentSocketRef struct {
	Provider AgentProviderID `json:"provider,omitempty"`
	Socket   string          `json:"socket"`
}

// AgentProviderObservation is a stat-only view of an agent provider. It never
// executes the provider or queries its agent.
type AgentProviderObservation struct {
	Provider      AgentProviderID `json:"provider"`
	Label         string          `json:"label"`
	Socket        string          `json:"socket"`
	SocketPresent bool            `json:"socket_present"`
	AppDetected   bool            `json:"app_detected"`
}

type agentProviderSpec struct {
	id     AgentProviderID
	label  string
	enable string
	// sockets are home-relative; apps starting with "~/" are home-relative and
	// all others are system paths.
	sockets map[string][]string
	apps    map[string][]string
}

var agentProviderSpecs = []agentProviderSpec{
	{
		id: AgentProviderBitwarden, label: "Bitwarden", enable: "open Bitwarden → Settings and enable the SSH agent",
		sockets: map[string][]string{
			"darwin": {"Library/Containers/com.bitwarden.desktop/Data/.bitwarden-ssh-agent.sock", ".bitwarden-ssh-agent.sock"},
			"linux":  {".bitwarden-ssh-agent.sock", "snap/bitwarden/current/.bitwarden-ssh-agent.sock", ".var/app/com.bitwarden.desktop/data/.bitwarden-ssh-agent.sock"},
		},
		apps: map[string][]string{
			"darwin": {"/Applications/Bitwarden.app", "~/Applications/Bitwarden.app"},
			"linux":  {"/snap/bitwarden", "/var/lib/flatpak/app/com.bitwarden.desktop", "~/.local/share/flatpak/app/com.bitwarden.desktop", "/opt/Bitwarden"},
		},
	},
	{
		id: AgentProvider1Password, label: "1Password", enable: "open 1Password → Settings → Developer and use the SSH agent",
		sockets: map[string][]string{
			"darwin": {"Library/Group Containers/2BUA8C4S2C.com.1password/t/agent.sock", ".1password/agent.sock"},
			"linux":  {".1password/agent.sock"},
		},
		apps: map[string][]string{
			"darwin": {"/Applications/1Password.app", "~/Applications/1Password.app"},
			"linux":  {"/opt/1Password"},
		},
	},
	{
		id: AgentProviderSecretive, label: "Secretive", enable: "open Secretive and finish its agent setup",
		sockets: map[string][]string{"darwin": {"Library/Containers/com.maxgoedjen.Secretive.SecretAgent/Data/socket.ssh"}},
		apps:    map[string][]string{"darwin": {"/Applications/Secretive.app", "~/Applications/Secretive.app"}},
	},
}

// AgentProviderLabel returns a display name for a provider.
func AgentProviderLabel(id AgentProviderID) string {
	for _, spec := range agentProviderSpecs {
		if spec.id == id {
			return spec.label
		}
	}
	switch id {
	case AgentProviderWindowsPipe:
		return "Windows OpenSSH agent pipe"
	case AgentProviderCustom:
		return "custom agent"
	}
	return string(id)
}

// AgentProviderEnableHint names the native step that exposes a provider's socket.
func AgentProviderEnableHint(id AgentProviderID) string {
	for _, spec := range agentProviderSpecs {
		if spec.id == id {
			return spec.enable
		}
	}
	return ""
}

// ObserveAgentProviders reports providers whose application or agent socket is
// present for goos. It only stats paths: sockets must be real sockets owned by
// the current user below directories that are not symlinks.
func (s *Service) ObserveAgentProviders(goos string) []AgentProviderObservation {
	if goos == "windows" {
		// Stat cannot attest a named pipe's server identity or ownership. Native
		// OpenSSH's ambient agent remains available, but explicit selection is
		// unavailable until a verified pipe backend exists.
		return nil
	}
	var observations []AgentProviderObservation
	for _, spec := range agentProviderSpecs {
		sockets := spec.sockets[goos]
		if len(sockets) == 0 {
			continue
		}
		observation := AgentProviderObservation{
			Provider: spec.id, Label: spec.label,
			Socket: filepath.Join(s.paths.Home, filepath.FromSlash(sockets[0])),
		}
		for _, relative := range sockets {
			path := filepath.Join(s.paths.Home, filepath.FromSlash(relative))
			if s.homeAgentSocketUsable(path) == nil {
				observation.Socket, observation.SocketPresent = path, true
				break
			}
		}
		for _, app := range spec.apps[goos] {
			if s.agentAppExists(app) {
				observation.AppDetected = true
				break
			}
		}
		if observation.SocketPresent || observation.AppDetected {
			observations = append(observations, observation)
		}
	}
	return observations
}

// ResolveAgentSocket maps a provider name or an absolute socket path to an
// agent reference. Provider names require a present socket.
func (s *Service) ResolveAgentSocket(goos, value string) (AgentSocketRef, error) {
	value = strings.TrimSpace(value)
	for _, spec := range agentProviderSpecs {
		if !strings.EqualFold(value, string(spec.id)) {
			continue
		}
		for _, observation := range s.ObserveAgentProviders(goos) {
			if observation.Provider == spec.id && observation.SocketPresent {
				return AgentSocketRef{Provider: spec.id, Socket: observation.Socket}, nil
			}
		}
		return AgentSocketRef{}, fmt.Errorf("%s SSH agent socket was not found; %s: %w", spec.label, spec.enable, ErrAgentProviderUnavailable)
	}
	if err := ValidateAgentSocketPath(value); err != nil {
		return AgentSocketRef{}, err
	}
	if err := s.agentSocketUsable(value); err != nil {
		return AgentSocketRef{}, fmt.Errorf("SSH agent socket %s: %w: %w", value, err, ErrAgentProviderUnavailable)
	}
	return AgentSocketRef{Provider: AgentProviderCustom, Socket: value}, nil
}

// ValidateAgentSocketPath accepts only an absolute, clean, token-free path so
// the same value can be written to SSH configuration and compared with ssh -G.
func ValidateAgentSocketPath(value string) error {
	if value == "" || !validUTF8NoControl(value) || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "%$~") {
		return fmt.Errorf("SSH agent socket must be an absolute path without ~, %% or $ tokens: %w", ErrUnsafePath)
	}
	return nil
}

type agentSocketIdentity struct {
	leaf    secureFileIdentity
	parents []secureFileIdentity
	anchor  platformfs.Anchor
}

func (s *Service) homeAgentSocketUsable(path string) error {
	return s.agentSocketUsable(path)
}

func (s *Service) agentSocketUsable(path string) error {
	_, err := s.captureAgentSocket(path)
	return err
}

func (s *Service) captureAgentSocket(path string) (*agentSocketIdentity, error) {
	if err := ValidateAgentSocketPath(path); err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" && s.agentSocketCheck == nil {
		return nil, fmt.Errorf("explicit Windows SSH agent pipes cannot yet be attested; use native OpenSSH agent configuration: %w", ErrManualRemediation)
	}
	anchor, err := platformfs.Resolve(path)
	if err != nil {
		return nil, err
	}
	// Home is already the service's fixed user-controlled trust boundary.
	if relative, err := filepath.Rel(s.paths.Home, path); err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
		if err := validateHomeDirectory(s.paths.Home); err != nil {
			return nil, err
		}
		anchor = platformfs.Anchor{Path: s.paths.Home}
	}
	identity := &agentSocketIdentity{anchor: anchor}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if len(identity.parents) >= 64 {
			return nil, fmt.Errorf("SSH agent directory depth exceeds the safety limit: %w", ErrUnsafePath)
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("%s is not a direct directory: %w", dir, ErrUnsafePath)
		}
		if err := platformRejectReparsePath(dir, false); err != nil {
			return nil, err
		}
		if err := platformAgentDirectory(dir, info); err != nil {
			return nil, err
		}
		identity.parents = append(identity.parents, secureFileIdentity{path: dir, info: info})
		if samePath(dir, anchor.Path) {
			break
		}
		if dir == filepath.Dir(dir) {
			return nil, ErrUnsafePath
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	check := s.agentSocketCheck
	if check == nil {
		check = platformAgentSocket
	}
	if err := check(path, info); err != nil {
		return nil, err
	}
	identity.leaf = secureFileIdentity{path: path, info: info}
	if err := identity.anchor.Verify(); err != nil {
		return nil, err
	}
	return identity, nil
}

func (s *Service) revalidateAgentSocket(identity *agentSocketIdentity) error {
	if identity == nil {
		return nil // Ambient/native agent compatibility; explicit agents are bound.
	}
	if err := identity.anchor.Verify(); err != nil {
		return fmt.Errorf("selected SSH agent anchor changed: %w", ErrSourceChanged)
	}
	current, err := s.captureAgentSocket(identity.leaf.path)
	if err != nil {
		return fmt.Errorf("selected SSH agent socket changed: %w", ErrSourceChanged)
	}
	if !sameSelectedKeyFileInfo(identity.leaf.info, current.leaf.info) || len(identity.parents) != len(current.parents) {
		return fmt.Errorf("selected SSH agent socket changed: %w", ErrSourceChanged)
	}
	for index, parent := range identity.parents {
		other := current.parents[index]
		if parent.path != other.path || !os.SameFile(parent.info, other.info) || parent.info.Mode() != other.info.Mode() {
			return fmt.Errorf("selected SSH agent directory changed: %w", ErrSourceChanged)
		}
	}
	return nil
}

// SelectAgentKey selects only the exact requested socket and fingerprint. It
// never discovers local private keys or silently substitutes another agent.
func (s *Service) SelectAgentKey(ctx context.Context, ref AgentSocketRef, fingerprint string) (KeyCandidate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return KeyCandidate{}, err
	}
	identity, err := s.captureAgentSocket(ref.Socket)
	if err != nil {
		return KeyCandidate{}, errors.Join(ErrAgentProviderUnavailable, err)
	}
	agent := &keyAgentContext{socket: ref.Socket, identity: identity}
	records, diagnostics, err := s.readAgentKeys(ctx, agent, "ssh-add selected-agent key")
	if err != nil {
		return KeyCandidate{}, err
	}
	if len(diagnostics) != 0 {
		return KeyCandidate{}, fmt.Errorf("selected SSH agent is unavailable or incompletely observed: %w", ErrSourceChanged)
	}
	for _, record := range records {
		if record.metadata.Fingerprint != fingerprint {
			continue
		}
		candidate := s.bindKeyMaterial(KeyCandidate{
			Source: KeySourceAgent, Sources: []KeySource{KeySourceAgent},
			Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment,
			Fingerprint: fingerprint, Provenance: KeyProvenance{Agent: true}, Agent: &ref,
		}, record.normalized)
		candidate.state.agent = agent
		return candidate, nil
	}
	return KeyCandidate{}, fmt.Errorf("selected key is not present in the requested SSH agent: %w", ErrSourceChanged)
}

// VerifyAgentKeyPolicy checks whether ordinary native authentication already
// permits this explicit agent key. It reads public selectors only and does not
// log in, repair configuration, or grant authentication evidence.
func (s *Service) VerifyAgentKeyPolicy(ctx context.Context, alias string, candidate KeyCandidate) error {
	if ctx == nil {
		ctx = context.Background()
	}
	material, err := s.validateKeyCandidate(candidate)
	if err != nil {
		return err
	}
	if material.agent == nil || material.safe.Agent == nil || material.safe.Provenance.Private || material.safe.Provenance.SecurityKeyStub {
		return ErrAgentPolicyMismatch
	}
	effective, err := s.Effective(ctx, alias)
	if err != nil {
		return err
	}
	if !s.effectiveUsesAgent(effective, material.agent, true) {
		return errors.Join(ErrAgentPolicyMismatch, ErrManualRemediation)
	}
	if effective.IdentitiesOnly != nil && *effective.IdentitiesOnly {
		matched := false
		for _, value := range effective.IdentityFiles {
			path, err := s.resolveSSHKeyPath(value)
			if err != nil {
				continue
			}
			if !strings.HasSuffix(strings.ToLower(path), ".pub") {
				path += ".pub"
			}
			record, err := s.readPublicKeyFile(path)
			if err == nil && record.metadata.Fingerprint == material.safe.Fingerprint {
				matched = true
				break
			}
		}
		if !matched {
			return errors.Join(ErrAgentPolicyMismatch, ErrManualRemediation)
		}
	}
	return s.revalidateSelectedKeySources(ctx, material)
}

func (s *Service) effectiveUsesAgent(effective EffectiveConfig, agent *keyAgentContext, exactAmbient bool) bool {
	configured, enabled, err := s.resolveIdentityAgent(firstEffectiveValue(effective, "identityagent"))
	if err != nil || !enabled {
		return false
	}
	if configured == "" && exactAmbient {
		configured = os.Getenv("SSH_AUTH_SOCK")
	}
	return configured == agent.socket || configured == "" && !exactAmbient
}

func (s *Service) agentAppExists(app string) bool {
	path := app
	if strings.HasPrefix(app, "~/") {
		path = filepath.Join(s.paths.Home, filepath.FromSlash(app[2:]))
	} else if s.agentSystemRoot != "" {
		path = filepath.Join(s.agentSystemRoot, filepath.FromSlash(app))
	}
	_, err := os.Stat(path)
	return err == nil
}

func cloneAgentRef(ref *AgentSocketRef) *AgentSocketRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}

func agentSocketMode(info fs.FileInfo) bool { return info.Mode().Type() == fs.ModeSocket }
