package sshdiscovery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

func candidateID(source, scope, nativeID string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + scope + "\x00" + nativeID))
	return source + ":" + hex.EncodeToString(sum[:16])
}

func safeText(text string, limit int) bool {
	if len(text) > limit || !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validDNS(name string) bool {
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func sortCandidates(candidates []Candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.ID < b.ID
	})
}

// ResolveCandidate selects one exact ID, IP, FQDN, or unique short name.
// There is no fuzzy matching, DNS resolution, first-match fallback, or mutation.
// A shared IP on multiple ports is intentionally ambiguous; use its exact ID.
func ResolveCandidate(candidates []Candidate, selector string) (Candidate, error) {
	if selector == "" || !safeText(selector, 1024) {
		return Candidate{}, ErrNotFound
	}
	var exact []Candidate
	for _, candidate := range candidates {
		if candidate.ID == selector {
			exact = append(exact, candidate)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		return Candidate{}, ErrAmbiguous
	}
	lookup := strings.TrimSuffix(selector, ".")
	ip, ipErr := netip.ParseAddr(lookup)
	var found []Candidate
	for _, candidate := range candidates {
		match := false
		if ipErr == nil {
			for _, address := range candidate.Addresses {
				parsed, err := netip.ParseAddr(address)
				if err == nil && parsed == ip {
					match = true
				}
			}
		} else {
			dns := strings.TrimSuffix(candidate.DNSName, ".")
			match = dns != "" && strings.EqualFold(dns, lookup)
			if !strings.Contains(lookup, ".") {
				short, _, _ := strings.Cut(dns, ".")
				match = match || short != "" && strings.EqualFold(short, lookup) || strings.EqualFold(candidate.Name, lookup)
			}
		}
		if match {
			found = append(found, candidate)
		}
	}
	if len(found) == 0 {
		return Candidate{}, fmt.Errorf("%w: %q", ErrNotFound, selector)
	}
	if len(found) != 1 {
		return Candidate{}, fmt.Errorf("%w: %q matches %d endpoints", ErrAmbiguous, selector, len(found))
	}
	return found[0], nil
}
