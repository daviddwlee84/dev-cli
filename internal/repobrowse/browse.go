// Package repobrowse selects a repository homepage using local Git evidence.
package repobrowse

import (
	"context"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

type Choice struct {
	Remote string
	URL    string
}

// Choices does not fetch or execute a forge provider. An empty selection
// leaves multiple candidates for the caller to present, rather than guessing.
func Choices(ctx context.Context, path, remote string) ([]Choice, error) {
	topology, err := gitx.RecoveryTopologyOf(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(topology.Remotes) == 0 {
		return nil, fmt.Errorf("repository has no remote")
	}
	selected := remote
	if selected == "" {
		status, err := gitx.StatusOf(ctx, path)
		if err != nil {
			return nil, err
		}
		for _, branch := range topology.Branches {
			if branch.Branch == status.Branch && branch.Remote != "." {
				selected = branch.Remote
			}
		}
		if selected == "" {
			for _, r := range topology.Remotes {
				if r.Name == "origin" {
					selected = r.Name
				}
			}
		}
	}
	var choices []Choice
	found := false
	for _, r := range topology.Remotes {
		if selected != "" && r.Name != selected {
			continue
		}
		found = true
		seen := map[string]bool{}
		for _, raw := range r.FetchURLs {
			web, ok := forge.DeriveWebURL(forge.WebURLRequest{Remote: raw})
			if !ok {
				return nil, fmt.Errorf("remote %q has no supported repository web URL (check its host or SSH alias)", r.Name)
			}
			if !seen[web.URL] {
				choices = append(choices, Choice{r.Name, web.URL})
				seen[web.URL] = true
			}
		}
		if len(seen) != 1 {
			return nil, fmt.Errorf("remote %q has ambiguous fetch URLs", r.Name)
		}
	}
	if !found {
		return nil, fmt.Errorf("remote %q is not configured", selected)
	}
	return choices, nil
}

func Names(choices []Choice) string {
	names := make([]string, len(choices))
	for i, c := range choices {
		names[i] = c.Remote
	}
	return strings.Join(names, ", ")
}
