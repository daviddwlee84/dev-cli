package sshvault

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
)

const (
	maxOutput = 1 << 20
	// This is a schema pin for the experimental implementation, not a claim of
	// live-vault validation. Broaden it only after reviewing the provider schema.
	BitwardenSchemaVersion = "2026.3.0"
)

var (
	opIDPattern = regexp.MustCompile(`^[a-z0-9]{26}$`)
	bwIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

func validText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || value != strings.TrimSpace(value) || strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// ValidateRequest checks request syntax and experimental opt-in only. It never
// inspects environment, files, accounts, sessions or providers, and grants no
// Plan/Apply authority or claim of native-context readiness.
func ValidateRequest(request Request) error { return validateRequest(request) }

func validateRequest(request Request) error {
	if !validText(request.Title, 256) {
		return ErrUnsafe
	}
	switch request.Provider {
	case Bitwarden:
		if !request.Experimental {
			return ErrExperimental
		}
		if request.AccountID != "" && !bwIDPattern.MatchString(request.AccountID) || request.VaultID != "" && request.VaultID != PersonalVault {
			return ErrUnsafe
		}
	case OnePassword:
		if request.Experimental || request.NativeContextApproved || !opIDPattern.MatchString(request.VaultID) || request.AccountID != "" && !opIDPattern.MatchString(request.AccountID) {
			return ErrUnsafe
		}
	default:
		return ErrUnsafe
	}
	return nil
}

func serverURL(value string, allowBareHost bool) (string, bool) {
	if !validText(value, 512) {
		return "", false
	}
	if allowBareHost && !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", false
	}
	return strings.TrimSuffix(parsed.String(), "/"), true
}

func supportedOPVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "2" {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 4 {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	minor, err := strconv.Atoi(parts[1])
	return err == nil && minor >= 20
}

// run never exposes a provider's stderr, raw failure, or partial output. A
// response can contain private fields even when only public metadata was asked
// for; every caller must wipe returned bytes after its bounded, typed decode.
func (s *Service) run(ctx context.Context, name string, args []string, input []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.runner == nil {
		return nil, ErrUnavailable
	}
	body, err := s.runner.Run(ctx, name, args, input)
	if err != nil || len(body) > maxOutput {
		sshcredential.Wipe(body)
		return nil, ErrUnavailable
	}
	return body, nil
}

func (s *Service) version(ctx context.Context, provider Provider) (string, error) {
	if provider != OnePassword {
		return "", ErrUnsupportedContext
	}
	body, err := s.run(ctx, "op", []string{"--version"}, nil)
	defer sshcredential.Wipe(body)
	if err != nil {
		return "", err
	}
	if len(body) > 64 {
		return "", ErrUnsupportedVersion
	}
	version := strings.TrimSpace(string(body))
	if !supportedOPVersion(version) {
		return "", ErrUnsupportedVersion
	}
	return version, nil
}

func (s *Service) observe(ctx context.Context, request Request) (Destination, string, error) {
	if err := ctx.Err(); err != nil {
		return Destination{}, "", err
	}
	version, err := s.version(ctx, request.Provider)
	if err != nil {
		return Destination{}, "", err
	}
	var destination Destination
	if request.Provider == OnePassword {
		destination, err = s.observeOnePassword(ctx, request)
	} else {
		err = ErrUnsupportedContext
	}
	return destination, version, err
}

func (s *Service) observeOnePassword(ctx context.Context, request Request) (Destination, error) {
	args := []string{"whoami", "--format", "json"}
	if request.AccountID != "" {
		args = append(args, "--account", request.AccountID)
	}
	body, err := s.run(ctx, "op", args, nil)
	defer sshcredential.Wipe(body)
	if err != nil {
		return Destination{}, ErrLocked
	}
	var identity struct {
		AccountID string `json:"account_uuid"`
		UserID    string `json:"user_uuid"`
		URL       string `json:"url"`
	}
	if json.Unmarshal(body, &identity) != nil {
		return Destination{}, ErrUnavailable
	}
	server, valid := serverURL(identity.URL, true)
	if !opIDPattern.MatchString(identity.AccountID) || !opIDPattern.MatchString(identity.UserID) || !valid {
		return Destination{}, ErrUnavailable
	}
	if request.AccountID != "" && request.AccountID != identity.AccountID {
		return Destination{}, ErrStale
	}
	vaultBody, err := s.run(ctx, "op", []string{"vault", "get", request.VaultID, "--account", identity.AccountID, "--format", "json"}, nil)
	defer sshcredential.Wipe(vaultBody)
	if err != nil {
		return Destination{}, err
	}
	var vault struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(vaultBody, &vault) != nil || vault.ID != request.VaultID || !validText(vault.Name, 256) {
		return Destination{}, ErrUnavailable
	}
	return Destination{Provider: OnePassword, AccountID: identity.AccountID, UserID: identity.UserID, ServerURL: server, VaultID: vault.ID, VaultName: vault.Name}, nil
}

func (s *Service) Plan(ctx context.Context, request Request) (Plan, error) {
	if err := validateRequest(request); err != nil {
		return Plan{}, err
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if request.Provider == Bitwarden {
		return s.planBitwardenNative(ctx, request)
	}
	destination, version, err := s.observe(ctx, request)
	if err != nil {
		return Plan{}, err
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return Plan{}, ErrUnavailable
	}
	request.AccountID, request.VaultID = destination.AccountID, destination.VaultID
	return Plan{Destination: destination, Version: version, Title: request.Title, Experimental: request.Experimental, state: &planState{service: s, request: request, dest: destination, version: version, operation: hex.EncodeToString(nonce[:])}}, nil
}

func (s *Service) Apply(ctx context.Context, plan Plan) (Result, error) {
	result, err := s.apply(ctx, plan)
	if err == nil && result.Status == StatusCreated && result.Receipt != nil {
		result.state = &resultState{plan: plan.state, status: result.Status, binding: result.BindingStatus, native: result.NativeContextStatus, endpoints: result.EndpointStatus, receipt: *result.Receipt}
	}
	return result, err
}

func (s *Service) ownsPlan(plan Plan) bool {
	return s != nil && plan.state != nil && plan.state.service == s && plan.Destination == plan.state.dest && plan.Version == plan.state.version && plan.Title == plan.state.request.Title && plan.Experimental == plan.state.request.Experimental && nativePlanMatches(plan, plan.state)
}

func (s *Service) apply(ctx context.Context, plan Plan) (Result, error) {
	result := Result{Status: StatusNotStarted}
	if !s.ownsPlan(plan) {
		return result, ErrStale
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := plan.state
	if state.attempted {
		return result, ErrPlanUsed
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if state.dest.Provider == Bitwarden {
		return s.applyBitwardenNative(ctx, state)
	}
	destination, version, err := s.observe(ctx, state.request)
	if err != nil {
		return result, err
	}
	if destination != state.dest || version != state.version {
		return result, ErrStale
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	// No retry can be authorized after the mutating command is attempted, even
	// if its response is lost or the native provider reports an error.
	state.attempted = true
	result, err = s.createOnePassword(ctx, state)
	if result.Status != StatusCreated {
		return result, err
	}
	current, currentVersion, observationErr := s.observe(ctx, state.request)
	if observationErr != nil || current != state.dest || currentVersion != state.version {
		result.BindingStatus = BindingUnknown
		return result, ErrUnknown
	}
	result.BindingStatus = BindingVerified
	return result, err
}
