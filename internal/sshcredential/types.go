// Package sshcredential keeps SSH passwords in operation memory or explicitly
// selected credential providers. Local records contain policy and references only.
package sshcredential

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxSecretBytes = 64 << 10

var (
	ErrUnavailable = errors.New("credential provider unavailable")
	ErrLocked      = errors.New("credential provider locked or permission denied")
	ErrNotFound    = errors.New("credential not found")
	ErrUnsafe      = errors.New("unsafe credential scope or storage")
	ErrStale       = errors.New("credential policy changed since planning")
	ErrUnknown     = errors.New("credential provider mutation outcome unknown")
	ErrDenied      = errors.New("credential prompt does not match the selected context")
)

type Context struct {
	Origin  string `json:"origin" toml:"origin"`
	Profile string `json:"profile" toml:"profile"`
	Route   string `json:"route" toml:"route"`
	Host    string `json:"host" toml:"host"`
	User    string `json:"user" toml:"user"`
	Port    int    `json:"port" toml:"port"`
	Kind    string `json:"kind" toml:"kind"`
}

func cleanText(s string) bool {
	if s == "" || len(s) > 1024 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (c Context) Validate() error {
	for _, s := range []string{c.Origin, c.Profile, c.Route, c.Host, c.User} {
		if !cleanText(s) {
			return ErrUnsafe
		}
	}
	if c.Port < 1 || c.Port > 65535 || c.Kind != "password" {
		return ErrUnsafe
	}
	return nil
}
func (c Context) ID() string {
	if ip, err := netip.ParseAddr(c.Host); err == nil {
		c.Host = ip.String()
	} else {
		c.Host = strings.ToLower(strings.TrimSuffix(c.Host, "."))
	}
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Policy string

const (
	PolicyAsk   Policy = "ask"
	PolicyNever Policy = "never"
)

type Reference struct {
	Provider string `json:"provider" toml:"provider"`
	ID       string `json:"id" toml:"id"`
}
type Record struct {
	ID              string     `json:"id" toml:"id"`
	Context         Context    `json:"context" toml:"context"`
	Policy          Policy     `json:"policy" toml:"policy"`
	Reference       *Reference `json:"reference,omitempty" toml:"reference,omitempty"`
	State           string     `json:"state" toml:"state"`
	PendingProvider string     `json:"pending_provider,omitempty" toml:"pending_provider,omitempty"`
}
type Snapshot struct {
	SchemaVersion int      `json:"schema_version" toml:"schema_version"`
	Revision      uint64   `json:"revision" toml:"revision"`
	Records       []Record `json:"records" toml:"records"`
}
type Provider interface {
	ID() string
	Available(context.Context) error
	Get(context.Context, Context, Reference) ([]byte, error)
	Put(context.Context, Context, *Reference, []byte) (Reference, error)
	Delete(context.Context, Context, Reference) error
}

func validateSecret(secret []byte) error {
	if len(secret) == 0 || len(secret) > MaxSecretBytes || !utf8.Valid(secret) {
		return ErrUnsafe
	}
	for _, b := range secret {
		if b == 0 || b == '\n' || b == '\r' {
			return ErrUnsafe
		}
	}
	return nil
}
func Wipe(secret []byte) {
	for i := range secret {
		secret[i] = 0
	}
}
func validReference(ref Reference) bool {
	return (ref.Provider == "system" || ref.Provider == "bitwarden") && cleanText(ref.ID)
}
