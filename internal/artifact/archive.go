package artifact

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/daviddwlee84/dev-cli/internal/agenthistory"
	"github.com/daviddwlee84/dev-cli/internal/config"
)

func (s *Service) finalizeArchive(ctx context.Context, request FinalizeRequest, intent *Intent) (*Intent, error) {
	h, err := agenthistory.Open(ctx, agenthistory.Options{Root: intent.WorktreePath, StateDir: intent.ArchiveStateDir, GlobalHygiene: filepath.Join(config.ConfigHome(), "dev", "hygiene.toml")})
	if err != nil {
		return nil, err
	}
	if intent.Status == Finalized {
		commit, e := h.VerifyArchiveReceipt(ctx, intent.ArchivePlanID)
		if e != nil || commit != intent.ArchiveCommit {
			return nil, errors.New("finalized archive receipt is unavailable")
		}
		return intent, nil
	}
	if err = s.revalidate(ctx, intent); err != nil {
		return nil, s.fail(ctx, intent.ID, "git-drift", err)
	}
	policy, err := h.PolicyFingerprint(ctx)
	if err != nil || policy != intent.ArchivePolicy {
		return nil, s.fail(ctx, intent.ID, "policy-drift", errors.New("archive policy changed after preparation"))
	}
	if intent.SessionEndedAt.IsZero() && !request.WriterStopped {
		return nil, s.fail(ctx, intent.ID, "writer-unproven", errors.New("archive finalization requires post-writer proof"))
	}
	planID := intent.ArchivePlanID
	if request.ArchivePlanID != "" {
		if planID != "" && planID != request.ArchivePlanID {
			return nil, errors.New("this intent already has an archive plan; inspect its receipt before replacing the handoff")
		}
		planID = request.ArchivePlanID
	}
	if planID == "" {
		if h.Binding.Protection == "redact" {
			return nil, fmt.Errorf("preview and review `dev artifact archive --session %s:%s`, then finalize with --archive-plan <id>", intent.Provider, intent.SessionID)
		}
		p, e := h.PreviewArchive(ctx, agenthistory.ArchiveOptions{Session: intent.Provider + ":" + intent.SessionID})
		if e != nil {
			return nil, s.fail(ctx, intent.ID, "archive-preview", e)
		}
		planID = p.ID
	}
	if err = h.CheckSessionPlan(ctx, planID, intent.Provider+":"+intent.SessionID); err != nil {
		return nil, err
	}
	if err = s.Store.Update(ctx, intent.ID, func(current *Intent) error { current.ArchivePlanID = planID; current.Status = Finalizing; return nil }); err != nil {
		return nil, err
	}
	result, err := h.ApplyArchive(ctx, planID, agenthistory.ApplyOptions{WriterStopped: true, Guard: request.Guard})
	if err != nil {
		return nil, s.fail(ctx, intent.ID, "archive-apply", err)
	}
	path, err := h.LocateSession(ctx, intent.Provider+":"+intent.SessionID)
	if err != nil {
		return nil, s.fail(ctx, intent.ID, "archive-source", err)
	}
	if err = s.Store.Update(ctx, intent.ID, func(current *Intent) error {
		current.Status = Finalized
		current.ArchivePlanID = planID
		current.ArchiveCommit = result.ArchiveCommit
		current.TranscriptPath = path
		current.FailureCode = ""
		return nil
	}); err != nil {
		return nil, err
	}
	return s.Store.Get(intent.ID)
}

// InspectHistoryReadiness checks external capture independently of the legacy
// commit receipt reconciler. It never changes old intent status or commits.
func InspectHistoryReadiness(ctx context.Context, store *Store, checkout string) (*agenthistory.Readiness, error) {
	if ctx == nil || store == nil {
		return nil, errors.New("history readiness requires context and store")
	}
	if _, e := os.Lstat(filepath.Join(checkout, ".dev-cli", "artifacts.toml")); errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	h, e := agenthistory.Open(ctx, agenthistory.Options{Root: checkout, StateDir: filepath.Dir(filepath.Dir(store.Dir))})
	if e != nil {
		return nil, e
	}
	proof, e := h.Readiness(ctx)
	return &proof, e
}
