package sshhost

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

func (s *Service) bootstrapKeySelections(ctx context.Context, request BootstrapRequest) (map[string]*keyMaterialState, *keyMaterialState, error) {
	materials := make(map[string]*keyMaterialState, len(request.HopKeys))
	var fallback *keyMaterialState
	if request.Key.Candidate.state != nil || request.Candidate.state != nil {
		material, err := s.bootstrapKeyMaterial(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		fallback = material
	} else if len(request.HopKeys) == 0 {
		return nil, nil, errors.New("bootstrap requires a selected key result or catalog candidate")
	}
	aliases := make([]string, 0, len(request.HopKeys))
	seen := make(map[string]bool, len(request.HopKeys))
	for alias := range request.HopKeys {
		if err := validateRouteLookupAlias(alias); err != nil {
			return nil, nil, err
		}
		folded := foldAlias(alias)
		if seen[folded] {
			return nil, nil, fmt.Errorf("duplicate per-hop key alias %q", alias)
		}
		seen[folded] = true
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		key := request.HopKeys[alias]
		if err := validateRouteLookupAlias(alias); err != nil {
			return nil, nil, err
		}
		folded := foldAlias(alias)
		if _, exists := materials[folded]; exists {
			return nil, nil, fmt.Errorf("duplicate per-hop key alias %q", alias)
		}
		material, err := s.bootstrapKeyMaterial(ctx, BootstrapRequest{Key: key, Interactive: request.Interactive})
		if err != nil {
			return nil, nil, fmt.Errorf("validate key for hop %q: %w", alias, err)
		}
		materials[folded] = material
	}
	return materials, fallback, nil
}
