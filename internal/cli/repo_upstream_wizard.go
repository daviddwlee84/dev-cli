package cli

import (
	"context"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/forge"
)

// promptUpstreamCreation is shared by repository acquisition/setup and Try
// graduation. Call it only after the user selects upstream creation: passive
// local and existing-URL workflows must never query forge authentication.
func promptUpstreamCreation(ctx context.Context, p *prompter, flags *repoBootstrapFlags) (bool, error) {
	var ready []forge.Readiness
	if flags.forge == "" || flags.forge == "auto" {
		ready = forge.ProbeAll(ctx)
	} else {
		ready = []forge.Readiness{forge.Probe(ctx, forge.Kind(flags.forge))}
	}
	var available []forge.Readiness
	for _, candidate := range ready {
		if candidate.Ready() {
			available = append(available, candidate)
		} else {
			fmt.Fprintf(p.out, "  %s: %s (%s)\n", candidate.Forge, candidate.Status, candidate.Action)
		}
	}
	if len(available) == 0 {
		return false, nil
	}
	selected := available[0].Forge
	if len(available) > 1 {
		choice, err := p.choiceOf("Forge", string(selected), []string{"github", "gitlab"},
			map[string]string{"github": "github", "gh": "github", "gitlab": "gitlab", "gl": "gitlab"})
		if err != nil {
			return false, err
		}
		selected = forge.Kind(choice)
	}
	flags.remote, flags.forge = true, string(selected)
	var err error
	flags.namespace, err = p.line("Owner / organization / namespace (optional)", flags.namespace)
	if err != nil {
		return false, err
	}
	visibility := flags.visibility
	if visibility == "" {
		visibility = "private"
		if flags.public {
			visibility = "public"
		}
	}
	options := []string{"private", "public"}
	choices := map[string]string{"private": "private", "public": "public"}
	if selected == forge.GitLab {
		options = append(options, "internal")
		choices["internal"] = "internal"
	}
	flags.visibility, err = p.choiceOf("Visibility", visibility, options, choices)
	if err != nil {
		return false, err
	}
	flags.push, err = p.confirm("Push the current branch commits?", flags.push)
	return err == nil, err
}
