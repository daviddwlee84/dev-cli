package artifact

import (
	"context"
	"sort"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// WorktreeInspection is the read-only aggregate of every artifact intent for a
// checkout. Ready means each intent was explicitly discarded or finalized to a
// commit still reachable from a recovery ref; it never changes an intent.
type WorktreeInspection struct {
	IntentCount int      `json:"intent_count"`
	Status      Status   `json:"status,omitempty"`
	Ready       bool     `json:"ready"`
	IntentIDs   []string `json:"intent_ids"`
}

// InspectWorktrees groups all intents once and verifies finalized commit
// reachability without reconciling or writing state.
func InspectWorktrees(ctx context.Context, store *Store) (map[string]WorktreeInspection, error) {
	intents, err := store.List()
	if err != nil {
		return nil, err
	}
	out := map[string]WorktreeInspection{}
	for _, intent := range intents {
		path, err := pathx.Canonical(intent.WorktreePath)
		if err != nil {
			continue
		}
		inspection, exists := out[path]
		if !exists {
			inspection.Ready = true
		}
		inspection.IntentCount++
		inspection.IntentIDs = append(inspection.IntentIDs, intent.ID)
		if statusPriority(intent.Status) >= statusPriority(inspection.Status) {
			inspection.Status = intent.Status
		}
		ready := intent.Status == Discarded ||
			(intent.Status == Finalized && intent.ArtifactCommit != "" && CommitReachable(ctx, intent))
		inspection.Ready = inspection.Ready && ready
		out[path] = inspection
	}
	for path, inspection := range out {
		sort.Strings(inspection.IntentIDs)
		out[path] = inspection
	}
	return out, nil
}

// CommitReachable reports whether an intent's artifact commit is retained by
// its checkout HEAD, task branch, or base. It is read-only and shared by
// integration enforcement and closeout evidence.
func CommitReachable(ctx context.Context, intent Intent) bool {
	refs := []struct {
		dir string
		ref string
	}{
		{intent.WorktreePath, "HEAD"},
		{intent.RepoPath, intent.Branch},
		{intent.RepoPath, intent.Base},
	}
	for _, candidate := range refs {
		if candidate.dir == "" || candidate.ref == "" {
			continue
		}
		if _, err := gitx.Run(ctx, candidate.dir, "merge-base", "--is-ancestor", intent.ArtifactCommit, candidate.ref); err == nil {
			return true
		}
	}
	return false
}

func statusPriority(status Status) int {
	switch status {
	case Discarded, Finalized:
		return 1
	case Armed:
		return 2
	case Finalizing:
		return 3
	case Failed:
		return 4
	default:
		return 0
	}
}
