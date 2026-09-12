package hygiene

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshremote"
)

type candidateObservation struct {
	Source string
	At     *time.Time
	Stale  bool
}

// machineLiterals reads existing local caches only. It cannot construct a
// discovery/remote client, query a credential store, or refresh an observation.
func (s *Service) machineLiterals(ctx context.Context) ([]sshhost.PrivacyLiteral, map[string]candidateObservation, any, []string, error) {
	values := []sshhost.PrivacyLiteral{}
	metadata := map[string]candidateObservation{}
	if s.CacheDir == "" {
		return values, metadata, nil, []string{"machine_cache_not_configured"}, nil
	}
	now := time.Now()
	discovery, discoveryErr := sshdiscovery.ReadCache(ctx, s.CacheDir, now)
	fleet, fleetErr := sshremote.ReadCache(ctx, filepath.Join(s.CacheDir, "fleet"), now)
	var gaps []string
	if discoveryErr != nil {
		gaps = append(gaps, "discovery_cache_incomplete")
	}
	if fleetErr != nil {
		gaps = append(gaps, "fleet_cache_incomplete")
	}
	add := func(kind, value, source string, at time.Time, stale bool) {
		if value == "" || strings.ContainsAny(value, "*?!%$\x00\r\n") {
			return
		}
		values = append(values, sshhost.PrivacyLiteral{Kind: kind, Value: value})
		if _, ok := metadata[value]; !ok {
			metadata[value] = candidateObservation{source, &at, stale}
		}
	}
	for i := range discovery {
		r := &discovery[i]
		if !r.Complete {
			gaps = append(gaps, "discovery_observation_partial")
			discoveryErr = sshdiscovery.ErrInvalidData
			continue
		}
		for _, c := range r.Candidates {
			add("ssh-host", c.DNSName, r.Source+" cache", r.ObservedAt, r.Stale)
			for _, address := range c.Addresses {
				add("ssh-host", address, r.Source+" cache", r.ObservedAt, r.Stale)
			}
		}
		r.Stale = false // elapsed TTL is presentation, not a change of source bytes
	}
	for i := range fleet {
		r := &fleet[i]
		if !r.Inventory.Complete {
			gaps = append(gaps, "fleet_observation_partial")
			fleetErr = sshremote.ErrInvalidData
			continue
		}
		for _, p := range r.Inventory.Profiles {
			if p.State != "active" {
				continue
			}
			add("ssh-host", p.HostName, "fleet cache", r.Inventory.ObservedAt, r.Stale)
			add("ssh-user", p.User, "fleet cache", r.Inventory.ObservedAt, r.Stale)
		}
		r.Stale = false
	}
	if len(discovery) == 0 && len(fleet) == 0 {
		gaps = append(gaps, "machine_cache_empty")
	}
	binding := struct {
		Directory string
		Discovery []sshdiscovery.Report
		Fleet     []sshremote.CachedInventory
	}{s.CacheDir, discovery, fleet}
	return values, metadata, binding, gaps, errors.Join(discoveryErr, fleetErr)
}
