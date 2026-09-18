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
	IntentCount      int      `json:"intent_count"`
	Status           Status   `json:"status,omitempty"`
	Ready            bool     `json:"ready"`
	IntentIDs        []string `json:"intent_ids"`
	ObservationError string   `json:"observation_error,omitempty"`
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
		status := intent.Status
		var ready bool
		if intent.Destination == "co-commit" {
			evidence, observeErr := inspectCoCommitReadiness(ctx, path, intent)
			ready = observeErr == nil && evidence.Finalized && evidence.ReceiptReachable
			if observeErr != nil {
				inspection.ObservationError = SafeCoCommitError(observeErr).Error()
			} else if evidence.Finalized {
				status = Finalized
			}
		} else {
			ready = intent.Status == Discarded ||
				(intent.Status == Finalized && (intent.ArtifactCommit != "" || intent.ArchiveCommit != "") && CommitReachable(ctx, intent))
		}
		if statusPriority(status) >= statusPriority(inspection.Status) {
			inspection.Status = status
		}
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
	if intent.Destination == "co-commit" {
		evidence, err := inspectCoCommitReadiness(ctx, intent.WorktreePath, intent)
		return err == nil && evidence.Finalized && evidence.ReceiptReachable
	}
	if intent.Destination == "archive" {
		for _, root := range []string{intent.WorktreePath, intent.RepoPath} {
			if ready, e := receiptRemainsReachable(ctx, root, intent); e == nil && ready {
				return true
			}
		}
		return false
	}
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
