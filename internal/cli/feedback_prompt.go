package cli

import (
	"context"
	"errors"

	"github.com/daviddwlee84/dev-cli/internal/promptkit"
	"github.com/spf13/cobra"
)

func newPromptFeedbackFixCmd(app *App, mode promptMode, agentName *string, dryRun *bool) *cobra.Command {
	return &cobra.Command{Use: "feedback-fix <report-id>", Short: "Render a verified feedback repair handoff; agent launch requires --agent",
		Long: "Render private context for the report's verified repair checkout. For this recipe, run/open always require an explicit --agent profile, even when a default or sole profile is configured.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if mode != promptRender && value(agentName) == "" {
				return asUsageError(errors.New("feedback repair launch requires an explicit --agent profile"))
			}
			return executePrompt(app, mode, value(agentName), valueBool(dryRun), promptkit.RecipeFeedbackFix, func(_ context.Context) (promptkit.Snapshot, error) {
				report, body, err := feedbackStore(app).RepairContext(cmd.Context(), args[0], feedbackStartBackend{app})
				if err != nil {
					return promptkit.Snapshot{}, feedbackError(err)
				}
				checkout := report.Repair.Snapshot.Checkout
				return promptkit.Snapshot{Scope: "feedback", ContextVersion: 1, Target: &promptkit.Target{Kind: "feedback_repair", Name: report.ID, Path: checkout, WorkingDirectory: checkout}, WorkingDirectory: checkout, Context: struct {
					Report      any    `json:"report"`
					PublicDraft string `json:"public_draft"`
				}{report, body}}, nil
			})
		}}
}
