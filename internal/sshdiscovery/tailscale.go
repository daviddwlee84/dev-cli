package sshdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

const tailscaleTimeout = 5 * time.Second
const maxTailscaleBytes = 4 << 20
const maxTailscalePeers = 4096

// The optional JSON contract is deliberately small and version-tested against
// Tailscale v1.102.3 ipn/ipnstate. Unknown future fields are ignored. The wire
// name sshHostKeys is lowercase despite the upstream Go name SSH_HostKeys.
type tailscaleStatus struct {
	BackendState   string
	Self           *tailscalePeer
	Peer           json.RawMessage
	CurrentTailnet *struct {
		Name           string
		MagicDNSSuffix string
	}
}

type tailscalePeer struct {
	ID           string
	HostName     string
	DNSName      string
	OS           string
	TailscaleIPs []string
	Online       *bool
	LastSeen     *time.Time
	SSHHostKeys  []string `json:"sshHostKeys"`
	ShareeNode   bool
}

// Tailscale invokes only `tailscale status --json`. It never enables or logs in
// to Tailscale, changes DNS, pings a peer, or starts an SSH authentication flow.
func (s *Service) Tailscale(ctx context.Context) (Report, error) {
	ctx, cancel := context.WithTimeout(nonNilContext(ctx), tailscaleTimeout)
	defer cancel()
	report := s.report(SourceTailscale, "tailscale")
	result, err := s.runner.Run(ctx, sshhost.RunRequest{
		Name: "tailscale", Args: []string{"status", "--json"}, Display: "Tailscale peer discovery",
	})
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			report.Status = StatusUnavailable
			return report, fmt.Errorf("tailscale is optional and was not found: %w", ErrUnavailable)
		}
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		return report, errors.New("could not query the local Tailscale daemon")
	}
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	if result.ExitCode != 0 {
		return report, errors.New("Tailscale status failed; check the local daemon and login state")
	}
	if result.StdoutTruncated || len(result.Stdout) > maxTailscaleBytes {
		return report, fmt.Errorf("Tailscale status output exceeds discovery bounds: %w", ErrInvalidData)
	}
	var status tailscaleStatus
	if err := json.Unmarshal(result.Stdout, &status); err != nil {
		return report, fmt.Errorf("cannot decode Tailscale status: %w", ErrInvalidData)
	}
	if status.BackendState != "Running" {
		report.Status = StatusUnavailable
		return report, fmt.Errorf("Tailscale is not running: %w", ErrUnavailable)
	}
	if status.Self == nil || status.Self.ID == "" || !safeText(status.Self.ID, 256) || len(status.Peer) == 0 {
		return report, fmt.Errorf("Tailscale status lacks local identity or peer data: %w", ErrInvalidData)
	}
	report.Scope = "self:" + status.Self.ID
	if status.CurrentTailnet != nil && status.CurrentTailnet.MagicDNSSuffix != "" {
		if !validDNS(status.CurrentTailnet.MagicDNSSuffix) {
			return report, fmt.Errorf("invalid tailnet DNS scope: %w", ErrInvalidData)
		}
		report.Scope += "/tailnet:" + strings.ToLower(strings.TrimSuffix(status.CurrentTailnet.MagicDNSSuffix, "."))
	}
	var peers map[string]json.RawMessage
	if err := json.Unmarshal(status.Peer, &peers); err != nil || len(peers) > maxTailscalePeers {
		return report, fmt.Errorf("invalid or oversized Tailscale peer collection: %w", ErrInvalidData)
	}
	complete := true
	seen := map[string]int{}
	for _, raw := range peers {
		var peer tailscalePeer
		if err := json.Unmarshal(raw, &peer); err != nil {
			complete = false
			continue
		}
		if peer.ID == status.Self.ID || peer.ShareeNode {
			continue
		}
		candidate, valid := candidateFromTailscale(peer, report.Scope)
		if !valid {
			complete = false
			continue
		}
		seen[candidate.ID]++
		report.Candidates = append(report.Candidates, candidate)
	}
	filtered := report.Candidates[:0]
	for _, candidate := range report.Candidates {
		if seen[candidate.ID] != 1 {
			complete = false
			continue
		}
		filtered = append(filtered, candidate)
	}
	report.Candidates = filtered
	sortCandidates(report.Candidates)
	report.Complete = complete
	report.Status = StatusReady
	if !complete {
		report.Status = StatusPartial
		return report, fmt.Errorf("some Tailscale peers have invalid or duplicate identities: %w", ErrInvalidData)
	}
	return report, nil
}

func candidateFromTailscale(peer tailscalePeer, scope string) (Candidate, bool) {
	candidate := Candidate{Source: SourceTailscale, Scope: scope, NativeID: peer.ID,
		Port: 22, Online: peer.Online, LastSeen: peer.LastSeen, State: StateDiscovered,
		AdvertisesSSH: len(peer.SSHHostKeys) != 0}
	if peer.ID == "" || !safeText(peer.ID, 256) || !safeText(peer.HostName, 256) || !safeText(peer.OS, 64) {
		return Candidate{}, false
	}
	if peer.DNSName != "" && !validDNS(peer.DNSName) {
		return Candidate{}, false
	}
	if len(peer.TailscaleIPs) > 32 {
		return Candidate{}, false
	}
	candidate.Name, candidate.OS = peer.HostName, peer.OS
	candidate.DNSName = strings.TrimSuffix(peer.DNSName, ".")
	seen := map[netip.Addr]bool{}
	for _, raw := range peer.TailscaleIPs {
		ip, err := netip.ParseAddr(raw)
		if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
			return Candidate{}, false
		}
		ip = ip.Unmap()
		if !seen[ip] {
			candidate.Addresses = append(candidate.Addresses, ip.String())
			seen[ip] = true
		}
	}
	if len(candidate.Addresses) == 0 {
		return Candidate{}, false
	}
	sort.Slice(candidate.Addresses, func(i, j int) bool {
		a, _ := netip.ParseAddr(candidate.Addresses[i])
		b, _ := netip.ParseAddr(candidate.Addresses[j])
		if a.Is4() != b.Is4() {
			return a.Is4()
		}
		return a.Less(b)
	})
	if candidate.Name == "" {
		candidate.Name, _, _ = strings.Cut(candidate.DNSName, ".")
	}
	if candidate.Name == "" {
		candidate.Name = candidate.Addresses[0]
	}
	candidate.ID = candidateID(candidate.Source, scope, peer.ID)
	if candidate.LastSeen != nil && candidate.LastSeen.IsZero() {
		candidate.LastSeen = nil
	}
	return candidate, true
}
