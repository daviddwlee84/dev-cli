package sshhost

import (
	"context"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// PrivacyLiteral is a lexical candidate, not a connection or authentication fact.
// Values never marshal into ordinary inventory JSON.
type PrivacyLiteral struct {
	Kind   string
	Value  string `json:"-"`
	Source Location
}

// PrivacyLiterals reads the same bounded static Include closure as discovery.
// No ssh -G, Match exec, DNS, private-key read or remote call occurs.
func (s *Service) PrivacyLiterals(ctx context.Context) ([]PrivacyLiteral, bool, error) {
	inventory, err := s.Discover(ctx)
	if err != nil {
		return nil, false, err
	}
	values := []PrivacyLiteral{}
	seen := map[string]bool{}
	complete := inventory.Complete
	for _, file := range inventory.Files {
		if seen[file.Path] {
			continue
		}
		seen[file.Path] = true
		data, err := safefile.ReadStablePath(ctx, file.Path, defaultMaxFileBytes)
		if err != nil {
			return nil, false, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			key, args, _, err := parseConfigLine(line)
			if err != nil {
				complete = false
				continue
			}
			kind := ""
			switch strings.ToLower(key) {
			case "host":
				kind = "ssh-alias"
			case "hostname":
				kind = "ssh-host"
			case "user":
				kind = "ssh-user"
			case "identityfile":
				kind = "ssh-key-path"
			}
			if kind == "" {
				continue
			}
			for _, v := range args {
				if v == "" || strings.ContainsAny(v, "*?!%$") {
					continue
				}
				values = append(values, PrivacyLiteral{kind, v, Location{file.Path, i + 1}})
			}
		}
	}
	return values, complete, nil
}
