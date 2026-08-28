package gitx

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Remote describes every configured fetch/push location for one Git remote.
// Multiple URLs are preserved because Git can fetch from one place and push to
// several destinations.
type RemoteInfo struct {
	Name      string   `json:"name"`
	FetchURLs []string `json:"fetch_urls,omitempty"`
	PushURLs  []string `json:"push_urls,omitempty"`
}

// BranchUpstream is one local branch and the tracking ref configured for it.
type BranchUpstream struct {
	Branch   string `json:"branch"`
	OID      string `json:"oid"`
	Upstream string `json:"upstream,omitempty"`
	Remote   string `json:"remote,omitempty"`
}

// RecoveryTopology is repository-wide publication structure. It deliberately
// does not claim that matching names mean the commits are backed up; reclaim
// performs remote-ref containment checks against this inventory later.
type RecoveryTopology struct {
	Remotes  []RemoteInfo     `json:"remotes"`
	Branches []BranchUpstream `json:"branches"`

	LocalOnlyBranches []string `json:"local_only_branches,omitempty"`
	UpstreamRemotes   []string `json:"upstream_remotes,omitempty"`
}

// HasRemote reports whether any remote is configured.
func (t RecoveryTopology) HasRemote() bool { return len(t.Remotes) > 0 }

// MultipleRemotes distinguishes several configured remotes from branches that
// merely use different upstreams.
func (t RecoveryTopology) MultipleRemotes() bool { return len(t.Remotes) > 1 }

// MultipleUpstreams reports local branches tracking more than one remote.
func (t RecoveryTopology) MultipleUpstreams() bool { return len(t.UpstreamRemotes) > 1 }

// Summary is the compact CLI/TUI form. Local-only branches remain visible even
// when a repository has one or more remotes.
func (t RecoveryTopology) Summary() string {
	if len(t.Remotes) == 0 {
		if len(t.LocalOnlyBranches) > 0 {
			return fmt.Sprintf("none · local:%d", len(t.LocalOnlyBranches))
		}
		return "none"
	}
	names := make([]string, len(t.Remotes))
	for index, remote := range t.Remotes {
		names[index] = remote.Name
	}
	summary := strings.Join(names, ",")
	if len(t.LocalOnlyBranches) > 0 {
		summary += fmt.Sprintf(" · local:%d", len(t.LocalOnlyBranches))
	}
	return summary
}

// RecoveryTopologyOf reads all configured remotes and every local branch's
// upstream. Two Git processes cover the repository regardless of branch count.
func RecoveryTopologyOf(ctx context.Context, dir string) (RecoveryTopology, error) {
	remoteOutput, err := run(ctx, dir, "remote", "-v")
	if err != nil {
		return RecoveryTopology{}, err
	}
	branchOutput, err := run(ctx, dir, "for-each-ref",
		"--format=%(refname:short)%00%(objectname)%00%(upstream:short)%00%(upstream:remotename)",
		"refs/heads")
	if err != nil {
		return RecoveryTopology{}, err
	}

	topology := RecoveryTopology{Remotes: parseRemoteInventory(remoteOutput)}
	configured := make(map[string]struct{}, len(topology.Remotes))
	for _, remote := range topology.Remotes {
		configured[remote.Name] = struct{}{}
	}
	upstreamRemotes := map[string]struct{}{}
	for _, line := range strings.Split(branchOutput, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 4 {
			return RecoveryTopology{}, fmt.Errorf("parse branch upstream record %q", line)
		}
		branch := BranchUpstream{
			Branch: fields[0], OID: fields[1], Upstream: fields[2], Remote: fields[3],
		}
		topology.Branches = append(topology.Branches, branch)
		_, remoteConfigured := configured[branch.Remote]
		if branch.Upstream == "" || branch.Remote == "" || branch.Remote == "." || !remoteConfigured {
			topology.LocalOnlyBranches = append(topology.LocalOnlyBranches, branch.Branch)
			continue
		}
		upstreamRemotes[branch.Remote] = struct{}{}
	}
	for remote := range upstreamRemotes {
		topology.UpstreamRemotes = append(topology.UpstreamRemotes, remote)
	}
	sort.Strings(topology.UpstreamRemotes)
	sort.Strings(topology.LocalOnlyBranches)
	sort.Slice(topology.Branches, func(i, j int) bool { return topology.Branches[i].Branch < topology.Branches[j].Branch })
	return topology, nil
}

func parseRemoteInventory(output string) []RemoteInfo {
	byName := map[string]*RemoteInfo{}
	for _, line := range strings.Split(output, "\n") {
		name, rest, ok := strings.Cut(line, "\t")
		if !ok || name == "" {
			continue
		}
		kind := ""
		switch {
		case strings.HasSuffix(rest, " (fetch)"):
			kind = "fetch"
			rest = strings.TrimSuffix(rest, " (fetch)")
		case strings.HasSuffix(rest, " (push)"):
			kind = "push"
			rest = strings.TrimSuffix(rest, " (push)")
		default:
			continue
		}
		remote := byName[name]
		if remote == nil {
			remote = &RemoteInfo{Name: name}
			byName[name] = remote
		}
		switch kind {
		case "fetch":
			remote.FetchURLs = appendUnique(remote.FetchURLs, rest)
		case "push":
			remote.PushURLs = appendUnique(remote.PushURLs, rest)
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	remotes := make([]RemoteInfo, 0, len(names))
	for _, name := range names {
		remote := *byName[name]
		sort.Strings(remote.FetchURLs)
		sort.Strings(remote.PushURLs)
		remotes = append(remotes, remote)
	}
	return remotes
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}
