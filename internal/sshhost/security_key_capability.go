package sshhost

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type securityKeyFile struct {
	requested string
	resolved  string
	info      fs.FileInfo
}

type securityKeyState struct {
	keygen    securityKeyFile
	client    securityKeyFile
	provider  *securityKeyFile
	options   SecurityKeyOptions
	mu        sync.Mutex
	attempted bool
	receipt   KeyResult
}

// ObserveSecurityKeyCapability performs only path/stat checks. It never invokes
// ssh, ssh-keygen, sc_auth, a provider library, or an authenticator.
func (s *Service) ObserveSecurityKeyCapability(ctx context.Context, provider string) (SecurityKeyCapability, error) {
	observation, _, err := s.observeSecurityKeyCapability(ctx, provider)
	return observation, err
}

func (s *Service) observeSecurityKeyCapability(ctx context.Context, provider string) (SecurityKeyCapability, *securityKeyState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SecurityKeyCapability{}, nil, err
	}
	if provider == "" {
		provider = "internal"
	}
	observation := SecurityKeyCapability{Status: "unavailable", Provider: provider}
	blocked := func(code, message string) (SecurityKeyCapability, *securityKeyState, error) {
		observation.Diagnostics = []Diagnostic{{Code: code, Message: message, BlocksMutation: true}}
		return observation, nil, nil
	}
	if provider == "apple-secure-enclave" {
		observation.Status = "unsupported"
		return blocked("secure_enclave_generation_unverified", "Automatic Apple Secure Enclave enrollment is experimental and unverified; use Secretive or an existing security-key stub. dev will not run sc_auth or enumerate resident keys.")
	}
	if provider != "internal" {
		if err := ValidateAgentSocketPath(provider); err != nil {
			return blocked("security_key_provider_unsafe", "SecurityKeyProvider must be internal or a safe absolute library path.")
		}
	}
	lookup := s.securityKeyLookPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	state := &securityKeyState{}
	for _, tool := range []struct {
		name   string
		target *securityKeyFile
		public *string
	}{
		{"ssh-keygen", &state.keygen, &observation.KeygenPath},
		{"ssh", &state.client, &observation.SSHClientPath},
	} {
		path, err := lookup(tool.name)
		if err != nil {
			return blocked("security_key_tool_unavailable", "A safe "+tool.name+" executable is required for hardware generation and subsequent native SSH authentication.")
		}
		file, err := s.inspectSecurityKeyFile(path, true)
		if err != nil {
			return blocked("security_key_tool_unsafe", "The selected "+tool.name+" executable failed local path/metadata checks.")
		}
		*tool.target, *tool.public = file, file.resolved
	}
	if provider != "internal" {
		file, err := s.inspectSecurityKeyFile(provider, false)
		if err != nil {
			return blocked("security_key_provider_unsafe", "The selected security-key provider library failed local path/metadata checks.")
		}
		state.provider = &file
		provider, observation.Provider = file.resolved, file.resolved
	}
	goos := s.securityKeyGOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if knownInternalFIDOUnsupported(goos, provider, state.keygen.resolved, state.client.resolved) {
		observation.Status = "unsupported"
		return blocked("ssh_client_lacks_fido", "Apple's stock internal OpenSSH provider lacks USB FIDO support. Select compatible ssh and ssh-keygen tools together; dev does not install or switch them automatically.")
	}
	observation.Status, observation.CanAttempt = "unknown", true
	observation.Diagnostics = []Diagnostic{{Code: "security_key_support_unknown", Message: "Tool and provider files are present, but compiled FIDO support and hardware availability are unknown. An explicitly reviewed interactive attempt may create a resident hardware credential even if later validation fails."}}
	state.options.Provider = provider
	return observation, state, nil
}

func knownInternalFIDOUnsupported(goos, provider, keygen, client string) bool {
	return goos == "darwin" && provider == "internal" && (keygen == "/usr/bin/ssh-keygen" || client == "/usr/bin/ssh")
}

func (s *Service) inspectSecurityKeyFile(path string, executable bool) (securityKeyFile, error) {
	if path == "" || !filepath.IsAbs(path) || !validUTF8NoControl(path) {
		return securityKeyFile{}, ErrUnsafePath
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return securityKeyFile{}, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return securityKeyFile{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return securityKeyFile{}, ErrUnsafePath
	}
	if s.securityKeyToolCheck != nil {
		if err := s.securityKeyToolCheck(resolved, info); err != nil {
			return securityKeyFile{}, err
		}
	} else {
		if runtime.GOOS == "windows" {
			return securityKeyFile{}, ErrManualRemediation
		}
		if err := validateSecurityKeyFileMetadata(resolved, info, executable); err != nil {
			return securityKeyFile{}, err
		}
		for dir, depth := filepath.Dir(resolved), 0; ; dir, depth = filepath.Dir(dir), depth+1 {
			if depth >= 64 {
				return securityKeyFile{}, ErrUnsafePath
			}
			parent, err := os.Lstat(dir)
			if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
				return securityKeyFile{}, ErrUnsafePath
			}
			if err := platformAgentDirectory(dir, parent); err != nil {
				return securityKeyFile{}, err
			}
			if dir == filepath.Dir(dir) || samePath(dir, s.paths.Home) {
				break
			}
		}
	}
	return securityKeyFile{requested: filepath.Clean(path), resolved: resolved, info: info}, nil
}

func validateSecurityKeyFileMetadata(path string, info fs.FileInfo, executable bool) error {
	// Sticky directories can protect their entries; a sticky regular file has
	// no such protection and must never inherit that exception.
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || executable && info.Mode().Perm()&0o111 == 0 {
		return ErrUnsafePath
	}
	return platformAgentDirectory(path, info)
}

func (s *Service) revalidateSecurityKeyState(state *securityKeyState) error {
	if state == nil {
		return nil
	}
	lookup := s.securityKeyLookPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	for _, tool := range []struct {
		name string
		file securityKeyFile
	}{{"ssh-keygen", state.keygen}, {"ssh", state.client}} {
		path, err := lookup(tool.name)
		if err != nil || filepath.Clean(path) != tool.file.requested {
			return fmt.Errorf("selected %s changed: %w", tool.name, ErrSourceChanged)
		}
		current, err := s.inspectSecurityKeyFile(path, true)
		if err != nil || current.resolved != tool.file.resolved || !sameSelectedKeyFileInfo(current.info, tool.file.info) {
			return fmt.Errorf("selected %s changed: %w", tool.name, ErrSourceChanged)
		}
	}
	if state.provider != nil {
		current, err := s.inspectSecurityKeyFile(state.provider.requested, false)
		if err != nil || current.resolved != state.provider.resolved || !sameSelectedKeyFileInfo(current.info, state.provider.info) {
			return fmt.Errorf("selected security-key provider changed: %w", ErrSourceChanged)
		}
	}
	return nil
}

func normalizeSecurityKeyOptions(options SecurityKeyOptions) (SecurityKeyOptions, error) {
	if options.Provider == "" {
		options.Provider = "internal"
	}
	if options.Application == "" {
		options.Application = "ssh:dev"
	}
	if !strings.HasPrefix(options.Application, "ssh:") || len(options.Application) < 5 || len(options.Application) > 68 {
		return options, errors.New("security-key application must be ssh: followed by 1-64 letters, digits, dots, underscores or dashes")
	}
	for _, c := range options.Application[4:] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return options, errors.New("invalid security-key application")
		}
	}
	return options, nil
}

// VerifySecurityKeyPolicy checks a foreign alias without changing its provider.
// The plan is service-bound and no native enrollment or login occurs here.
func (s *Service) VerifySecurityKeyPolicy(ctx context.Context, alias string, plan KeyPlan) error {
	if plan.state == nil || plan.state.serviceID != s.id || !equalKeyPlans(plan, plan.state.public) || plan.state.hardware == nil {
		return ErrBlocked
	}
	if err := s.revalidateSecurityKeyState(plan.state.hardware); err != nil {
		return err
	}
	effective, err := s.Effective(ctx, alias)
	if err != nil {
		return err
	}
	if !securityKeyProviderMatches(effective, plan.state.hardware) {
		return errors.Join(ErrManualRemediation, ErrSecurityKeyPolicyMismatch)
	}
	return s.revalidateSecurityKeyState(plan.state.hardware)
}

func selectedSSHClient(selector keySelector) string {
	if selector.hardware != nil {
		return selector.hardware.client.resolved
	}
	return "ssh"
}

func securityKeyProviderMatches(effective EffectiveConfig, state *securityKeyState) bool {
	provider := firstEffectiveValue(effective, "securitykeyprovider")
	if provider == "" {
		provider = "internal"
	}
	return provider == state.options.Provider || state.provider != nil && provider == state.provider.requested
}
