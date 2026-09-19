package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/config"
)

type artifactHandoffRow struct {
	artifact.Intent
	CoCommitObservation *artifact.CoCommitObservation `json:"co_commit_observation,omitempty"`
	ObservationError    *artifact.CoCommitError       `json:"observation_error,omitempty"`
}

func renderArtifactHandoffs(app *App, intents []artifact.Intent, jsonOutput bool) error {
	rows := make([]artifactHandoffRow, 0, len(intents))
	for _, intent := range intents {
		row := artifactHandoffRow{Intent: intent}
		if intent.Destination == "co-commit" {
			observation, err := artifact.ObserveCoCommit(ctxOf(), &intent)
			if err != nil {
				row.ObservationError = coCommitPublicError(err)
			} else {
				row.CoCommitObservation = &observation
			}
		}
		rows = append(rows, row)
	}
	if jsonOutput {
		return json.NewEncoder(app.Out).Encode(struct {
			Kind          string               `json:"kind"`
			SchemaVersion int                  `json:"schema_version"`
			Handoffs      []artifactHandoffRow `json:"handoffs"`
		}{"artifact_handoffs", 1, rows})
	}
	style := app.outStyle()
	table := app.newTable("INTENT", "STATUS", "HELPER", "SESSION", "BRANCH", "COMMIT")
	for _, row := range rows {
		helper, commit := "-", firstNonEmpty(row.ArtifactCommit, row.ArchiveCommit)
		if row.ObservationError != nil {
			helper = "unknown"
		} else if row.CoCommitObservation != nil {
			helper = row.CoCommitObservation.State
			if row.CoCommitObservation.CommitProven {
				commit = row.CoCommitObservation.CommitOID
			}
		}
		table.Add(row.ID, style.artifactState(string(row.Status)), helper,
			style.dim(row.Provider+":"+shortOID(row.SessionID)), row.Branch, style.dim(shortOID(commit)))
	}
	table.Render(app.Out)
	return nil
}

func renderArtifactPreparationJSON(app *App, intent *artifact.Intent) error {
	return json.NewEncoder(app.Out).Encode(struct {
		Kind          string           `json:"kind"`
		SchemaVersion int              `json:"schema_version"`
		Intent        *artifact.Intent `json:"intent"`
	}{"artifact_preparation", 1, intent})
}

func renderArtifactFinalizationJSON(app *App, intent *artifact.Intent) error {
	return json.NewEncoder(app.Out).Encode(struct {
		Kind          string           `json:"kind"`
		SchemaVersion int              `json:"schema_version"`
		Intent        *artifact.Intent `json:"intent"`
	}{"artifact_finalization", 1, intent})
}

func coCommitPublicError(err error) *artifact.CoCommitError {
	if err == nil {
		return nil
	}
	var result *artifact.CoCommitError
	if errors.As(err, &result) {
		return result
	}
	if errors.Is(err, artifact.ErrStaleRevision) {
		return &artifact.CoCommitError{Code: "stale_native_revision", NextAction: "review_current_native_intent"}
	}
	return &artifact.CoCommitError{Code: "observation_unavailable", NextAction: "inspect_private_local_state"}
}

func renderCoCommitResult(app *App, result *artifact.CoCommitResult, err error, jsonOutput bool) error {
	publicError := coCommitPublicError(err)
	if jsonOutput {
		if result == nil {
			result = &artifact.CoCommitResult{Kind: "co_commit_handoff", SchemaVersion: 1, Status: "blocked", NextAction: "inspect_private_local_state"}
		}
		if publicError != nil && result.RequestID == "" {
			result.NextAction = publicError.NextAction
		}
		output := struct {
			*artifact.CoCommitResult
			Error *artifact.CoCommitError `json:"error,omitempty"`
		}{result, publicError}
		if writeErr := json.NewEncoder(app.Out).Encode(output); writeErr != nil {
			return writeErr
		}
	} else if result != nil {
		// The real recorder must retain the canonical queue acknowledgement as
		// one complete JSON object, even when the surrounding CLI is human-facing.
		if result.QueueAck != nil {
			if writeErr := json.NewEncoder(app.Out).Encode(result.QueueAck); writeErr != nil {
				return writeErr
			}
		}
		fmt.Fprintf(app.Out, "CO-COMMIT %s\n", result.Status)
		if result.RequestID != "" {
			fmt.Fprintf(app.Out, "   request   %s\n", result.RequestID)
		}
		if result.Intent != nil {
			fmt.Fprintf(app.Out, "   intent    %s\n", result.Intent.ID)
			fmt.Fprintf(app.Out, "   worktree  %s\n", config.Contract(result.Intent.WorktreePath))
			if result.Intent.ArtifactCommit != "" {
				fmt.Fprintf(app.Out, "   commit    %s\n", shortOID(result.Intent.ArtifactCommit))
			}
		}
		fmt.Fprintf(app.Out, "   next      %s\n", result.NextAction)
		if result.Status == "queued" || result.Status == "queued_but_not_bound" {
			fmt.Fprintln(app.Out, `Report "finalization queued" and exit the agent; do not perform more repository/index operations.`)
		}
	}
	if publicError != nil {
		return publicError
	}
	return nil
}

func renderCoCommitReview(app *App, preview *artifact.CoCommitReview, err error, jsonOutput bool) error {
	if err != nil {
		return renderCoCommitResult(app, nil, err, jsonOutput)
	}
	if jsonOutput {
		return json.NewEncoder(app.Out).Encode(preview)
	}
	fmt.Fprintf(app.Out, "CO-COMMIT REVIEW %s\n", preview.NativeIntentID)
	fmt.Fprintf(app.Out, "   status    %s\n", preview.ReviewStatus)
	fmt.Fprintf(app.Out, "   findings  %d\n", len(preview.Findings))
	fmt.Fprintf(app.Out, "   revision  %s\n", preview.NativeRevision)
	fmt.Fprintf(app.Out, "   review    %s\n", config.Contract(preview.PrivateReviewPath))
	fmt.Fprintln(app.Out, "Review exact finding IDs privately; no commit or rotation is authorized by this preview.")
	return nil
}
