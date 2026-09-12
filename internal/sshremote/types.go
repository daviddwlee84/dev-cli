// Package sshremote exchanges bounded, versioned SSH metadata with explicitly
// selected fleet hosts. Profiles remain source-owned observations; importing a
// profile never transfers a private key or changes a fleet machine pin.
package sshremote

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/machineid"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

const (
	SchemaVersion            = 1
	ProtocolVersion          = 1
	MaxRequestBytes    int64 = 64 << 10
	MaxResponseBytes   int64 = 4 << 20
	MaxProfiles              = 4096
	MaxKeys                  = 4096
	MaxRouteHops             = 16
	CacheTTL                 = 5 * time.Minute
	StatusReady              = "ready"
	StatusPartial            = "partial"
	StatusUnavailable        = "unavailable"
	StatusIncompatible       = "incompatible"
	StatusUnreachable        = "unreachable"
	StatusTimeout            = "timeout"
	StatusInvalid            = "invalid-response"
	StatusStale              = "stale"
)

var (
	ErrUnavailable         = errors.New("remote dev SSH capability is unavailable")
	ErrIncompatible        = errors.New("remote dev SSH protocol is incompatible")
	ErrUnreachable         = errors.New("remote SSH source is unreachable")
	ErrTimeout             = errors.New("remote SSH metadata request timed out")
	ErrInvalidData         = errors.New("invalid remote SSH metadata")
	ErrSourceChanged       = errors.New("remote SSH source changed")
	ErrNotFound            = errors.New("remote SSH profile was not found")
	ErrUnsafeCache         = errors.New("unsafe remote SSH cache")
	ErrInteractionRequired = errors.New("remote SSH source requires configured interactive credentials")
	ErrUnsupported         = errors.New("remote SSH profile policy is unsupported")
)

type Header struct {
	SchemaVersion   int `json:"schema_version"`
	ProtocolVersion int `json:"protocol_version"`
}

func NewHeader() Header { return Header{SchemaVersion, ProtocolVersion} }
func (h Header) Validate() error {
	if h.SchemaVersion != SchemaVersion || h.ProtocolVersion != ProtocolVersion {
		return ErrIncompatible
	}
	return nil
}

// Origin distinguishes separate login accounts and config roots on one host.
// MachineID is observed identity, never an automatically accepted fleet pin.
type Origin struct {
	ID        string `json:"id"`
	MachineID string `json:"machine_id"`
	Platform  string `json:"platform"`
	User      string `json:"user"`
	Root      string `json:"root"`
}

func OriginID(machineID, user, root string) string {
	return digestID("ssh-origin", machineID, user, root)
}
func ProfileID(originID, alias string) string {
	return digestID("fleet-ssh", originID, strings.ToLower(alias))
}
func digestID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + ":" + hex.EncodeToString(digest[:16])
}
func (o Origin) Validate() error {
	if machineid.Validate(o.MachineID) != nil || !safeText(o.User, 1024, false) || !safeText(o.Root, 16384, false) ||
		(o.Platform != "darwin" && o.Platform != "linux" && o.Platform != "windows") || o.ID != OriginID(o.MachineID, o.User, o.Root) {
		return ErrInvalidData
	}
	return nil
}

type CapabilityRequest struct{ Header }
type Capability struct {
	Header
	Kind       string   `json:"kind"`
	Origin     Origin   `json:"origin"`
	Supported  bool     `json:"supported"`
	Operations []string `json:"operations"`
}

func (c Capability) Validate() error {
	if err := c.Header.Validate(); err != nil {
		return err
	}
	if c.Kind != "ssh_remote_capability" || c.Origin.Validate() != nil || !c.Supported || len(c.Operations) != 4 {
		return ErrInvalidData
	}
	want := []string{"inventory", "resolve", "keys", "connect"}
	for i := range want {
		if c.Operations[i] != want[i] {
			return ErrInvalidData
		}
	}
	return nil
}

type Request struct {
	Header
	OriginID  string `json:"origin_id"`
	MachineID string `json:"machine_id"`
}

func NewRequest(origin Origin) Request {
	return Request{Header: NewHeader(), OriginID: origin.ID, MachineID: origin.MachineID}
}
func (r Request) Validate() error {
	if err := r.Header.Validate(); err != nil {
		return err
	}
	if !validDigestID(r.OriginID, "ssh-origin") || machineid.Validate(r.MachineID) != nil {
		return ErrInvalidData
	}
	return nil
}

type Diagnostic struct {
	Code       string `json:"code"`
	Alias      string `json:"alias,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
}

// Profile contains only static hints. Unknown values remain absent.
type Profile struct {
	ID          string           `json:"id"`
	Alias       string           `json:"alias"`
	Fingerprint string           `json:"fingerprint"`
	State       string           `json:"state"`
	Source      sshhost.Location `json:"source"`
	HostName    string           `json:"host_name,omitempty"`
	User        string           `json:"user,omitempty"`
	Port        int              `json:"port,omitempty"`
}

func (p Profile) Selection(origin Origin) Selection {
	return Selection{OriginID: origin.ID, ProfileID: p.ID, Alias: p.Alias, Fingerprint: p.Fingerprint}
}
func (p Profile) validate(origin Origin) error {
	if sshhost.ValidateLookupAlias(p.Alias) != nil || p.ID != ProfileID(origin.ID, p.Alias) || !validFingerprint(p.Fingerprint) ||
		!safeText(p.Source.Path, 16384, false) || p.Source.Line < 1 || !safeText(p.HostName, 1024, true) || !safeText(p.User, 1024, true) || p.Port < 0 || p.Port > 65535 {
		return ErrInvalidData
	}
	switch p.State {
	case "active", "unknown", "inactive", "conflict":
	default:
		return ErrInvalidData
	}
	return nil
}

type Inventory struct {
	Header
	Kind        string       `json:"kind"`
	Origin      Origin       `json:"origin"`
	Complete    bool         `json:"complete"`
	ObservedAt  time.Time    `json:"observed_at"`
	Profiles    []Profile    `json:"profiles"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func (i Inventory) Validate() error {
	if err := validateEnvelope(i.Header, i.Kind, "ssh_remote_inventory", i.Origin, i.ObservedAt); err != nil {
		return err
	}
	if len(i.Profiles) > MaxProfiles || validateDiagnostics(i.Diagnostics) != nil {
		return ErrInvalidData
	}
	seen := map[string]bool{}
	for _, p := range i.Profiles {
		if p.validate(i.Origin) != nil || seen[p.ID] {
			return ErrInvalidData
		}
		seen[p.ID] = true
	}
	return nil
}
func (i Inventory) Find(aliasOrID string) (Profile, bool) {
	for _, p := range i.Profiles {
		if p.ID == aliasOrID || strings.EqualFold(p.Alias, aliasOrID) {
			return p, true
		}
	}
	return Profile{}, false
}

type Selection struct {
	OriginID    string `json:"origin_id"`
	ProfileID   string `json:"profile_id"`
	Alias       string `json:"alias"`
	Fingerprint string `json:"fingerprint"`
}

func (s Selection) Validate() error {
	if !validDigestID(s.OriginID, "ssh-origin") || sshhost.ValidateLookupAlias(s.Alias) != nil || s.ProfileID != ProfileID(s.OriginID, s.Alias) || !validFingerprint(s.Fingerprint) {
		return ErrInvalidData
	}
	return nil
}

type ResolveRequest struct {
	Request
	Selection Selection `json:"selection"`
}
type RouteProfile struct {
	OriginID  string           `json:"origin_id"`
	ProfileID string           `json:"profile_id"`
	Hop       sshhost.RouteHop `json:"hop"`
}
type Resolved struct {
	Header
	Kind              string                  `json:"kind"`
	Origin            Origin                  `json:"origin"`
	Profile           Profile                 `json:"profile"`
	ObservedAt        time.Time               `json:"observed_at"`
	Effective         sshhost.EffectiveConfig `json:"effective"`
	Route             sshhost.Route           `json:"route"`
	RouteProfiles     []RouteProfile          `json:"route_profiles"`
	Importable        bool                    `json:"importable"`
	ImportDiagnostics []Diagnostic            `json:"import_diagnostics,omitempty"`
}

func (r Resolved) Validate() error {
	if err := validateEnvelope(r.Header, r.Kind, "ssh_remote_resolved", r.Origin, r.ObservedAt); err != nil {
		return err
	}
	if r.Profile.validate(r.Origin) != nil || r.Profile.State != "active" || !strings.EqualFold(r.Profile.Alias, r.Effective.Alias) || !strings.EqualFold(r.Profile.Alias, r.Route.Alias) ||
		len(r.Route.Hops) == 0 || len(r.Route.Hops) > MaxRouteHops || len(r.RouteProfiles) != len(r.Route.Hops) || validateDiagnostics(r.ImportDiagnostics) != nil || r.Importable && len(r.ImportDiagnostics) != 0 {
		return ErrInvalidData
	}
	if !safeText(r.Effective.HostName, 1024, false) || !safeText(r.Effective.User, 1024, false) || r.Effective.Port < 1 || r.Effective.Port > 65535 || !safeText(r.Effective.ProxyJump, 4096, true) || len(r.Effective.IdentityFiles) > 128 {
		return ErrInvalidData
	}
	target := r.Route.Hops[len(r.Route.Hops)-1]
	if !strings.EqualFold(target.Alias, r.Profile.Alias) || target.HostName != r.Effective.HostName || target.User != r.Effective.User || target.Port != r.Effective.Port {
		return ErrInvalidData
	}
	if !validRemoteOS(r.Route.TargetRemoteOS) {
		return ErrInvalidData
	}
	for _, path := range r.Effective.IdentityFiles {
		if !safeText(path, 16384, false) {
			return ErrInvalidData
		}
	}
	seen := map[string]bool{}
	for n, hop := range r.Route.Hops {
		key := strings.ToLower(hop.Alias) + "\x00" + hop.User + "\x00" + strconv.Itoa(hop.Port)
		if sshhost.ValidateLookupAlias(hop.Alias) != nil || seen[key] || !safeText(hop.HostName, 1024, false) || !safeText(hop.User, 1024, false) || !safeText(hop.Reference, 2048, false) || hop.Port < 1 || hop.Port > 65535 || hop.Target != (n == len(r.Route.Hops)-1) || !validRemoteOS(hop.RemoteOS) {
			return ErrInvalidData
		}
		switch hop.AdminState {
		case "", sshhost.AdminUnknown, sshhost.AdminStandard, sshhost.AdminAdministrator:
		default:
			return ErrInvalidData
		}
		seen[key] = true
		profile := r.RouteProfiles[n]
		if profile.OriginID != r.Origin.ID || profile.ProfileID != ProfileID(r.Origin.ID, hop.Alias) || profile.Hop != hop {
			return ErrInvalidData
		}
	}
	return nil
}

type KeysRequest struct {
	Request
	Alias   string `json:"alias,omitempty"`
	NoAgent bool   `json:"no_agent"`
}
type Keys struct {
	Header
	Kind       string             `json:"kind"`
	Origin     Origin             `json:"origin"`
	Alias      string             `json:"alias,omitempty"`
	ObservedAt time.Time          `json:"observed_at"`
	Catalog    sshhost.KeyCatalog `json:"catalog"`
}

func (k Keys) Validate() error {
	if err := validateEnvelope(k.Header, k.Kind, "ssh_remote_keys", k.Origin, k.ObservedAt); err != nil {
		return err
	}
	if k.Alias != "" && sshhost.ValidateLookupAlias(k.Alias) != nil || len(k.Catalog.Candidates) > MaxKeys || len(k.Catalog.Diagnostics) > 4096 {
		return ErrInvalidData
	}
	seen := map[string]bool{}
	for _, c := range k.Catalog.Candidates {
		if !validKeyID(c.Fingerprint) || seen[c.Fingerprint] || !safeText(c.Algorithm, 128, false) || !safeText(c.Comment, 16384, true) || !safeText(c.PublicPath, 16384, true) || !safeText(c.IdentityFile, 16384, true) {
			return ErrInvalidData
		}
		if !validKeySource(c.Source) || len(c.Sources) > 8 {
			return ErrInvalidData
		}
		for _, source := range c.Sources {
			if !validKeySource(source) {
				return ErrInvalidData
			}
		}
		seen[c.Fingerprint] = true
	}
	for _, d := range k.Catalog.Diagnostics {
		if !safeText(d.Code, 128, false) || !safeText(d.Path, 16384, true) || !safeText(d.Message, 4096, true) {
			return ErrInvalidData
		}
	}
	return nil
}

// ConnectRequest has no command/argv/stdin field: v1 opens an existing alias.
type ConnectRequest struct {
	Request
	Selection Selection `json:"selection"`
	KeyID     string    `json:"key_id,omitempty"`
}

func (r ConnectRequest) Validate() error {
	if err := r.Request.Validate(); err != nil {
		return err
	}
	if r.Selection.Validate() != nil || r.Selection.OriginID != r.OriginID || r.KeyID != "" && !validKeyID(r.KeyID) {
		return ErrInvalidData
	}
	return nil
}
func EncodeConnectRequest(request ConnectRequest) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	data, err := fleet.MarshalBounded(request, MaxRequestBytes)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
func DecodeConnectRequest(encoded string) (ConnectRequest, error) {
	var request ConnectRequest
	if len(encoded) == 0 || len(encoded) > int(MaxRequestBytes)*4/3+4 {
		return request, ErrInvalidData
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return request, ErrInvalidData
	}
	if err := fleet.UnmarshalStrict(data, MaxRequestBytes, &request); err != nil {
		return request, ErrInvalidData
	}
	return request, request.Validate()
}

func EncodeSelector(host, alias string) string {
	return "fleet:" + url.PathEscape(host) + "/" + url.PathEscape(alias)
}
func ParseSelector(value string) (host, alias string, err error) {
	if !strings.HasPrefix(value, "fleet:") {
		return "", "", ErrInvalidData
	}
	parts := strings.Split(strings.TrimPrefix(value, "fleet:"), "/")
	if len(parts) != 2 {
		return "", "", ErrInvalidData
	}
	host, err = url.PathUnescape(parts[0])
	if err != nil {
		return "", "", ErrInvalidData
	}
	alias, err = url.PathUnescape(parts[1])
	if err != nil {
		return "", "", ErrInvalidData
	}
	if !safeText(host, 1024, false) || host != strings.TrimSpace(host) || sshhost.ValidateLookupAlias(alias) != nil {
		return "", "", ErrInvalidData
	}
	return host, alias, nil
}

func validKeyID(value string) bool {
	if !strings.HasPrefix(value, "SHA256:") {
		return false
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "SHA256:"))
	return err == nil && len(raw) == sha256.Size
}

func validRemoteOS(value sshhost.RemoteOS) bool {
	return value == "" || value == sshhost.RemoteOSUnknown || value == sshhost.RemoteOSPOSIX || value == sshhost.RemoteOSWindows
}

func validKeySource(value sshhost.KeySource) bool {
	switch value {
	case "", sshhost.KeySourceEffectiveIdentity, sshhost.KeySourcePublicFile, sshhost.KeySourceAgent, sshhost.KeySourceExplicit, sshhost.KeySourceGenerated, sshhost.KeySourceDerived:
		return true
	default:
		return false
	}
}
func validDigest(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size && value == strings.ToLower(value)
}

func validFingerprint(value string) bool { return validDigest(strings.TrimPrefix(value, "sha256:")) }
func validDigestID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix+":") {
		return false
	}
	raw := strings.TrimPrefix(value, prefix+":")
	data, err := hex.DecodeString(raw)
	return err == nil && len(data) == 16 && raw == strings.ToLower(raw)
}
func safeText(value string, limit int, empty bool) bool {
	return (empty || value != "") && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}
func validateEnvelope(header Header, kind, expected string, origin Origin, observed time.Time) error {
	if err := header.Validate(); err != nil {
		return err
	}
	if kind != expected || origin.Validate() != nil || observed.IsZero() {
		return ErrInvalidData
	}
	return nil
}
func validateDiagnostics(diagnostics []Diagnostic) error {
	if len(diagnostics) > 4096 {
		return ErrInvalidData
	}
	for _, d := range diagnostics {
		if !safeText(d.Code, 128, false) || !safeText(d.Alias, 255, true) {
			return ErrInvalidData
		}
	}
	return nil
}

// ErrorCode exposes stable classifications without reflecting remote output.
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return StatusTimeout
	case errors.Is(err, ErrUnavailable):
		return StatusUnavailable
	case errors.Is(err, ErrIncompatible):
		return StatusIncompatible
	case errors.Is(err, ErrUnreachable):
		return StatusUnreachable
	case errors.Is(err, ErrTimeout):
		return StatusTimeout
	case errors.Is(err, ErrSourceChanged), errors.Is(err, sshhost.ErrSourceChanged):
		return StatusStale
	case errors.Is(err, ErrNotFound):
		return "not-found"
	case errors.Is(err, ErrInteractionRequired), errors.Is(err, sshhost.ErrInteractionRequired):
		return "interaction-required"
	case errors.Is(err, ErrUnsupported), errors.Is(err, sshhost.ErrUnsupportedRoute):
		return "unsupported"
	default:
		return StatusInvalid
	}
}

func sourceDigest(value any) string {
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// ExitError preserves a child status without printing remote output twice.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("SSH connection exited %d", e.Code) }
func (e ExitError) ExitCode() int { return e.Code }
