package fleet

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

type SyncRequest struct {
	RemoteIdentity string `json:"remote_identity"`
	Branch         string `json:"branch"`
	ExpectedOID    string `json:"expected_oid"`
}

type SyncState string

const (
	SyncUpdated      SyncState = "updated"
	SyncCurrent      SyncState = "already-current"
	SyncFetched      SyncState = "fetched-only"
	SyncAbsent       SyncState = "absent"
	SyncNoDev        SyncState = "no-dev"
	SyncUnreachable  SyncState = "unreachable"
	SyncTimeout      SyncState = "timeout"
	SyncDirty        SyncState = "blocked-dirty"
	SyncAhead        SyncState = "blocked-ahead"
	SyncDiverged     SyncState = "blocked-diverged"
	SyncAmbiguous    SyncState = "ambiguous"
	SyncIncompatible SyncState = "incompatible"
	SyncFailed       SyncState = "failed"
)

type SyncResult struct {
	Host        string    `json:"host"`
	State       SyncState `json:"state"`
	Repo        string    `json:"repo,omitempty"`
	Path        string    `json:"path,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Remote      string    `json:"remote,omitempty"`
	BeforeOID   string    `json:"before_oid,omitempty"`
	AfterOID    string    `json:"after_oid,omitempty"`
	ExpectedOID string    `json:"expected_oid,omitempty"`
	Error       string    `json:"error,omitempty"`
}

func (r SyncResult) Ignored() bool { return r.State == SyncAbsent || r.State == SyncNoDev }

func (r SyncResult) Success() bool {
	switch r.State {
	case SyncUpdated, SyncCurrent, SyncFetched, SyncAbsent, SyncNoDev:
		return true
	default:
		return false
	}
}

type syncCandidate struct {
	repository repo.Repo
	remote     string
}

func ApplySync(ctx context.Context, cfg devconfig.Config, request SyncRequest) SyncResult {
	result := SyncResult{Host: devconfig.Hostname(), Branch: request.Branch, ExpectedOID: request.ExpectedOID}
	request.RemoteIdentity = catalog.NormalizeRemoteIdentity(request.RemoteIdentity)
	if request.RemoteIdentity == "" || request.Branch == "" || request.ExpectedOID == "" {
		result.State, result.Error = SyncFailed, "sync request requires remote identity, branch and expected OID"
		return result
	}
	repositories, err := repo.Discover(ctx, cfg.DiscoveryRoots(), repo.DefaultOptions())
	if err != nil {
		result.State, result.Error = SyncFailed, err.Error()
		return result
	}
	var candidates []syncCandidate
	for _, repository := range repositories {
		if repository.Bare || !repository.HasGit {
			continue
		}
		topology, topologyErr := gitx.RecoveryTopologyOf(ctx, repository.Path)
		if topologyErr != nil {
			continue
		}
		if remote := matchingRemoteAt(ctx, repository.Path, topology, request.RemoteIdentity); remote != "" {
			candidates = append(candidates, syncCandidate{repository: repository, remote: remote})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].repository.Path < candidates[j].repository.Path })
	switch len(candidates) {
	case 0:
		result.State = SyncAbsent
		return result
	case 1:
	default:
		result.State = SyncAmbiguous
		paths := make([]string, len(candidates))
		for index, candidate := range candidates {
			paths[index] = candidate.repository.Path
		}
		result.Error = "remote identity matches multiple clones: " + strings.Join(paths, ", ")
		return result
	}
	candidate := candidates[0]
	result.Repo, result.Path, result.Remote = candidate.repository.Display(), candidate.repository.Path, candidate.remote
	if _, err := gitx.Run(ctx, candidate.repository.Path, "fetch", "--prune", "--quiet", candidate.remote); err != nil {
		result.State, result.Error = SyncFailed, err.Error()
		return result
	}
	remoteRef := candidate.remote + "/" + request.Branch
	remoteOID, err := gitx.Run(ctx, candidate.repository.Path, "rev-parse", "--verify", remoteRef+"^{commit}")
	if err != nil {
		result.State, result.Error = SyncFailed, fmt.Sprintf("remote branch %s is unavailable after fetch: %v", remoteRef, err)
		return result
	}
	if _, err := gitx.Run(ctx, candidate.repository.Path, "merge-base", "--is-ancestor", request.ExpectedOID, remoteOID); err != nil {
		result.State, result.Error = SyncFailed, "fetched remote branch does not contain the source commit"
		return result
	}
	worktree, checkedOut, err := gitx.WorktreeFor(ctx, candidate.repository.Path, request.Branch)
	if err != nil {
		result.State, result.Error = SyncFailed, err.Error()
		return result
	}
	if !checkedOut {
		result.State, result.AfterOID = SyncFetched, remoteOID
		return result
	}
	result.Path = worktree.Path
	status, err := gitx.StatusOf(ctx, worktree.Path)
	if err != nil {
		result.State, result.Error = SyncFailed, err.Error()
		return result
	}
	if status.Dirty() || status.Conflicted > 0 {
		result.State, result.Error = SyncDirty, status.Summary()
		return result
	}
	localOID, err := gitx.Run(ctx, worktree.Path, "rev-parse", "HEAD")
	if err != nil {
		result.State, result.Error = SyncFailed, err.Error()
		return result
	}
	result.BeforeOID = localOID
	if localOID == remoteOID {
		result.State, result.AfterOID = SyncCurrent, localOID
		return result
	}
	if _, err := gitx.Run(ctx, worktree.Path, "merge-base", "--is-ancestor", localOID, remoteOID); err != nil {
		if _, aheadErr := gitx.Run(ctx, worktree.Path, "merge-base", "--is-ancestor", remoteOID, localOID); aheadErr == nil {
			result.State, result.Error = SyncAhead, "checked-out branch contains commits not on the fetched remote"
		} else {
			result.State, result.Error = SyncDiverged, "checked-out branch diverged; rebase or merge manually"
		}
		return result
	}
	if _, err := gitx.Run(ctx, worktree.Path, "merge", "--ff-only", remoteRef); err != nil {
		result.State, result.Error = SyncFailed, err.Error()
		return result
	}
	after, err := gitx.Run(ctx, worktree.Path, "rev-parse", "HEAD")
	if err != nil || after != remoteOID {
		result.State = SyncFailed
		result.Error = "fast-forward completed without reaching the fetched remote revision"
		return result
	}
	result.State, result.AfterOID = SyncUpdated, after
	return result
}

func matchingRemote(topology gitx.RecoveryTopology, identity string) string {
	var names []string
	for _, remote := range topology.Remotes {
		matched := false
		for _, raw := range append(append([]string{}, remote.FetchURLs...), remote.PushURLs...) {
			if catalog.NormalizeRemoteIdentity(raw) == identity {
				matched = true
				break
			}
		}
		if matched {
			names = append(names, remote.Name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func matchingRemoteAt(ctx context.Context, path string, topology gitx.RecoveryTopology, identity string) string {
	var names []string
	for _, remote := range topology.Remotes {
		matched := false
		for _, key := range []string{"remote." + remote.Name + ".url", "remote." + remote.Name + ".pushurl"} {
			raw, err := gitx.Run(ctx, path, "config", "--get-all", key)
			if err != nil {
				continue
			}
			for _, value := range strings.Split(raw, "\n") {
				if catalog.NormalizeRemoteIdentity(value) == identity {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if matched {
			names = append(names, remote.Name)
		}
	}
	if len(names) == 0 {
		return matchingRemote(topology, identity)
	}
	sort.Strings(names)
	return names[0]
}
