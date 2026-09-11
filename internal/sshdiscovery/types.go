// Package sshdiscovery discovers candidate SSH endpoints. Discovery records
// observations only: neither an advertised service nor an SSH banner proves
// authentication, and neither creates an OpenSSH alias or fleet registration.
package sshdiscovery

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

const (
	SourceTailscale   = "tailscale"
	SourceLAN         = "lan"
	StatusReady       = "ready"
	StatusPartial     = "partial"
	StatusUnavailable = "unavailable"
	StatusFailed      = "failed"

	StateDiscovered = "discovered"
	StateSSH        = "ssh_banner"
	StateOpen       = "open_unknown"

	CacheTTL = 5 * time.Minute
)

var (
	ErrUnavailable  = errors.New("discovery dependency unavailable")
	ErrInvalidData  = errors.New("invalid discovery data")
	ErrInvalidScope = errors.New("invalid or unavailable on-link discovery scope")
	ErrNotFound     = errors.New("discovery candidate not found")
	ErrAmbiguous    = errors.New("discovery candidate selection is ambiguous")
	ErrUnsafeCache  = errors.New("unsafe discovery cache")
)

// Candidate contains only connection suggestions and source observations.
// Online means control-plane presence for Tailscale, not successful SSH login.
// AdvertisesSSH is advisory; false never proves that Tailscale SSH is disabled.
type Candidate struct {
	ID            string     `json:"id"`
	Source        string     `json:"source"`
	Scope         string     `json:"scope"`
	NativeID      string     `json:"native_id,omitempty"`
	Name          string     `json:"name,omitempty"`
	DNSName       string     `json:"dns_name,omitempty"`
	Addresses     []string   `json:"addresses"`
	Port          int        `json:"port"`
	OS            string     `json:"os,omitempty"`
	Online        *bool      `json:"online,omitempty"`
	AdvertisesSSH bool       `json:"advertises_ssh"`
	LastSeen      *time.Time `json:"last_seen,omitempty"`
	State         string     `json:"state"`
}

// Report describes one explicit discovery attempt, including incomplete ones.
// Cached reports retain their observation time and are never authentication
// authority, including during the five-minute freshness window.
type Report struct {
	Source     string      `json:"source"`
	Status     string      `json:"status"`
	Complete   bool        `json:"complete"`
	ObservedAt time.Time   `json:"observed_at"`
	Stale      bool        `json:"stale"`
	Scope      string      `json:"scope"`
	Candidates []Candidate `json:"candidates"`
}

// InterfaceScope is an eligible, active, directly connected IPv4 network.
// Prefix may contain more than 256 addresses; callers must select an explicit
// bounded subset for LAN, rather than implicitly scanning the whole prefix.
type InterfaceScope struct {
	Interface string `json:"interface"`
	Index     int    `json:"index"`
	Address   string `json:"address"`
	Prefix    string `json:"prefix"`
}

// LANRequest always requires explicit IPv4 addresses or CIDR ranges. Interface
// may be omitted only when exactly one eligible interface contains every range.
type LANRequest struct {
	Interface string   `json:"interface,omitempty"`
	Ranges    []string `json:"ranges"`
	Ports     []int    `json:"ports,omitempty"`
}

// ServiceOptions supplies deterministic, network-free seams for callers and
// tests. The production bounds cannot be increased through these options.
type ServiceOptions struct {
	DialContext func(context.Context, string, string) (net.Conn, error)
	LookupAddr  func(context.Context, string) ([]string, error)
	Interfaces  func() ([]InterfaceScope, error)
	Now         func() time.Time
}

type Service struct {
	runner  sshhost.Runner
	options ServiceOptions
}

// NewService uses the optional CLI only on Tailscale, and the network only on
// LAN. Constructing a service performs no I/O. Later options override earlier
// non-nil hooks.
func NewService(runner sshhost.Runner, options ...ServiceOptions) *Service {
	if runner == nil {
		runner = sshhost.ExecRunner{}
	}
	s := &Service{runner: runner, options: ServiceOptions{
		DialContext: (&net.Dialer{}).DialContext,
		LookupAddr:  net.DefaultResolver.LookupAddr,
		Interfaces:  listInterfaces,
		Now:         time.Now,
	}}
	for _, option := range options {
		if option.DialContext != nil {
			s.options.DialContext = option.DialContext
		}
		if option.LookupAddr != nil {
			s.options.LookupAddr = option.LookupAddr
		}
		if option.Interfaces != nil {
			s.options.Interfaces = option.Interfaces
		}
		if option.Now != nil {
			s.options.Now = option.Now
		}
	}
	return s
}

func (s *Service) report(source, scope string) Report {
	return Report{Source: source, Scope: scope, Status: StatusFailed,
		ObservedAt: s.options.Now().UTC(), Candidates: []Candidate{}}
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
