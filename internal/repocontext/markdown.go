package repocontext

import (
	"fmt"
	"strings"
)

// FormatMarkdown extends the legacy deterministic handoff with additive safe
// remote, fleet, provenance, and readiness sections. legacy remains unchanged
// at the front so existing copy/paste consumers retain their headings.
func FormatMarkdown(report Report, legacy string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(legacy))
	fmt.Fprintf(&b, "\n\n## Context report\n\n- Schema: %d\n- Generated: `%s`\n", report.SchemaVersion, report.GeneratedAt.Format("2006-01-02T15:04:05Z07:00"))
	if report.SelectedCheckout != nil {
		fmt.Fprintf(&b, "- Selected checkout: `%s`", report.SelectedCheckout.Path)
		if report.SelectedCheckout.Branch != nil {
			fmt.Fprintf(&b, " (`%s`)", *report.SelectedCheckout.Branch)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("- Selected checkout: unavailable\n")
	}

	b.WriteString("\n## Readiness\n")
	for _, gate := range report.Assessment.Gates {
		fmt.Fprintf(&b, "\n- `%s`: **%s**", gate.Code, gate.Outcome)
		if len(gate.Reasons) > 0 {
			fmt.Fprintf(&b, " — %s", gate.Reasons[0].Detail)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n## Safe remotes\n")
	if len(report.Remotes) == 0 {
		b.WriteString("\n- No remotes were observed.\n")
	}
	for _, remote := range report.Remotes {
		fmt.Fprintf(&b, "\n### %s", remote.Name)
		if len(remote.Roles) > 0 {
			roles := make([]string, len(remote.Roles))
			for index, role := range remote.Roles {
				roles[index] = string(role)
			}
			fmt.Fprintf(&b, " — %s", strings.Join(roles, ", "))
		}
		b.WriteString("\n")
		writeEndpoints(&b, "fetch", remote.Fetch)
		writeEndpoints(&b, "push", remote.Push)
	}

	b.WriteString("\n## Fleet observations\n\n")
	fmt.Fprintf(&b, "- Coverage: **%s**\n", report.Fleet.Coverage)
	if report.Fleet.ConfiguredHosts == nil {
		b.WriteString("- Configured hosts: unavailable\n")
	} else {
		fmt.Fprintf(&b, "- Configured hosts: %d\n", *report.Fleet.ConfiguredHosts)
	}
	if len(report.Fleet.Hosts) == 0 && report.Fleet.ConfiguredHosts != nil && *report.Fleet.ConfiguredHosts == 0 {
		b.WriteString("- No fleet hosts are configured.\n")
	}
	sourceByID := map[string]Source{}
	for _, source := range report.Sources {
		sourceByID[source.ID] = source
	}
	for _, host := range report.Fleet.Hosts {
		fmt.Fprintf(&b, "- `%s`: %s; match %s", host.Name, host.State, host.Match)
		if host.SourceID != nil {
			if source, ok := sourceByID[*host.SourceID]; ok {
				fmt.Fprintf(&b, "; %s (%s, %s old)", source.Authority, source.Freshness, humanAge(source.AgeSeconds))
			}
		}
		if len(host.Repositories) == 1 {
			fmt.Fprintf(&b, "; `%s` on `%s`", host.Repositories[0].Display, host.Repositories[0].Path)
		} else if len(host.Repositories) > 1 {
			fmt.Fprintf(&b, "; %d exact-identity candidates", len(host.Repositories))
		}
		if host.Error != nil {
			fmt.Fprintf(&b, " — %s", *host.Error)
		}
		b.WriteString("\n")
	}

	if len(report.Errors) > 0 {
		b.WriteString("\n## Collection errors\n\n")
		for _, contextError := range report.Errors {
			fmt.Fprintf(&b, "- `%s` (%s/%s): %s\n", contextError.Code, contextError.Scope, contextError.Subject, contextError.Message)
		}
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func writeEndpoints(b *strings.Builder, kind string, endpoints []RemoteEndpoint) {
	if len(endpoints) == 0 {
		fmt.Fprintf(b, "\n- %s: unavailable\n", kind)
		return
	}
	for _, endpoint := range endpoints {
		fmt.Fprintf(b, "\n- %s: ", kind)
		if endpoint.Identity != nil {
			fmt.Fprintf(b, "`%s`", *endpoint.Identity)
		} else {
			b.WriteString("unavailable")
		}
		if endpoint.WebURL != nil {
			fmt.Fprintf(b, " — %s (%s, %s)", endpoint.WebURL.URL, endpoint.WebURL.Provider, endpoint.WebURL.Source)
		}
		if endpoint.Error != nil {
			fmt.Fprintf(b, " — %s", *endpoint.Error)
		}
		b.WriteString("\n")
	}
}

func humanAge(seconds int64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dd", seconds/86400)
	}
}
