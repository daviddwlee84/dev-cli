package artifact

import (
	"context"
	"path/filepath"
	"strings"
)

// CoCommitReview is an allowlisted projection, never a transparent proxy for
// arbitrary helper/scanner JSON. Matching bytes remain in the private receipt.
type CoCommitReview struct {
	SchemaVersion      int                     `json:"schema_version"`
	ProtocolVersion    int                     `json:"protocol_version"`
	Status             string                  `json:"status"`
	RequestRevision    string                  `json:"request_revision"`
	ReceiptRevision    string                  `json:"receipt_revision"`
	PreparedTree       string                  `json:"prepared_tree"`
	ReviewStatus       string                  `json:"review_status"`
	Reviewable         bool                    `json:"reviewable"`
	Findings           []CoCommitReviewFinding `json:"findings"`
	PrivateReceiptPath string                  `json:"private_receipt_path"`
	PrivateReviewPath  string                  `json:"private_review_path"`
	CommitAttempted    bool                    `json:"commit_attempted"`
	NativeIntentID     string                  `json:"native_intent_id"`
	NativeRevision     string                  `json:"native_revision"`
}

type CoCommitReviewFinding struct {
	FindingID       string `json:"finding_id"`
	Path            string `json:"path"`
	Kind            string `json:"kind"`
	MaskedValue     string `json:"masked_value"`
	OccurrenceCount int    `json:"occurrence_count"`
	Complete        bool   `json:"complete"`
}

func (p CoCommitReview) validate(intent *Intent, observation CoCommitObservation) error {
	if p.SchemaVersion != 2 || p.ProtocolVersion != 2 || p.Status != "review_preview" || p.CommitAttempted || p.RequestRevision != intent.CoCommit.RequestRevision || !coCommitDigest.MatchString(p.ReceiptRevision) || p.ReceiptRevision != observation.ReceiptRevision || p.PreparedTree != observation.PreparedTree || !coCommitOID.MatchString(p.PreparedTree) || len(p.Findings) > 10000 {
		return coCommitFailure("invalid_review_preview", "inspect_private_canonical_receipt")
	}
	switch p.ReviewStatus {
	case "not_required", "unresolved", "reviewed_noncredential", "credential_rotation_required", "stale":
	default:
		return coCommitFailure("invalid_review_preview", "inspect_private_canonical_receipt")
	}
	runDir := filepath.Dir(intent.CoCommit.RequestPath)
	for _, path := range []string{p.PrivateReceiptPath, p.PrivateReviewPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) != runDir || strings.ContainsAny(path, "\x00\r\n") {
			return coCommitFailure("invalid_review_path", "inspect_private_canonical_receipt")
		}
	}
	allowed := make(map[string]bool)
	for _, path := range append([]string{intent.SpecStoryPath}, intent.PlanPaths...) {
		relative, err := coCommitRelative(intent.WorktreePath, path)
		if err != nil {
			return err
		}
		allowed[relative] = true
	}
	seen := make(map[string]bool)
	for _, finding := range p.Findings {
		if !coCommitDigest.MatchString(finding.FindingID) || seen[finding.FindingID] || !allowed[finding.Path] || (finding.Kind != "scanner" && finding.Kind != "structure") || finding.MaskedValue != "[REDACTED]" || finding.OccurrenceCount < 1 || finding.OccurrenceCount > 10000000 || p.Reviewable && !finding.Complete {
			return coCommitFailure("invalid_review_finding", "inspect_private_canonical_receipt")
		}
		seen[finding.FindingID] = true
	}
	return nil
}

func (s *Service) PreviewCoCommitReview(ctx context.Context, intentID string) (*CoCommitReview, error) {
	if s.Store == nil {
		return nil, coCommitFailure("store_missing", "inspect_native_configuration")
	}
	record, err := s.Store.GetRecord(intentID)
	if err != nil {
		return nil, err
	}
	helper, err := boundCoCommitHelper(ctx, record.Intent)
	if err != nil {
		return nil, err
	}
	observation, err := helper.inspect(ctx, record.Intent.CoCommit.RequestPath, false)
	if err != nil {
		return nil, err
	}
	if err := coCommitBindingMatches(record.Intent, observation); err != nil {
		return nil, err
	}
	var preview CoCommitReview
	if err := helper.call(ctx, "finalize", []string{"--request", record.Intent.CoCommit.RequestPath, "--preview-review", "--json", "--expected-revision", observation.JournalRevision}, &preview); err != nil {
		return nil, err
	}
	if err := preview.validate(record.Intent, observation); err != nil {
		return nil, err
	}
	current, err := s.Store.GetRecord(intentID)
	if err != nil || current.Revision != record.Revision {
		return nil, ErrStaleRevision
	}
	preview.NativeIntentID, preview.NativeRevision = record.Intent.ID, record.Revision
	return &preview, nil
}
