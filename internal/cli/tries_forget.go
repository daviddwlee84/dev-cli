package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newTriesForgetCmd(app *App) *cobra.Command {
	var dryRun, jsonOut bool
	var confirm string
	cmd := &cobra.Command{Use: "forget <ref>", Short: "Preview forgetting an unreferenced Try whose local directory is confirmed missing", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := newTriageService(app)
		if err != nil {
			return err
		}
		plan, err := s.PlanForget(cmd.Context(), args[0], nil)
		if err != nil {
			if jsonOut {
				_ = json.NewEncoder(app.Out).Encode(map[string]any{"plan": plan, "error": err.Error(), "outcome": "not-applied"})
			}
			return err
		}
		if dryRun {
			if jsonOut {
				return json.NewEncoder(app.Out).Encode(plan)
			}
			fmt.Fprintf(app.Out, "Forget Try %s\n  missing path  %s\n  effect        remove catalog entry only; preserve project and note files\n", plan.ID, plan.Source)
			return nil
		}
		if confirm != plan.ID {
			if confirm != "" || !app.interactive() || jsonOut {
				return fmt.Errorf("forgetting requires --confirm-forget %s; use --dry-run to preview", plan.ID)
			}
			fmt.Fprintf(app.Out, "Forget Try %s\n  missing path  %s\n  effect        remove this catalog entry; no project or note files\n", plan.ID, plan.Source)
			answer, err := newPrompter(app).dangerLine("Type FORGET " + plan.ID + " to forget this entry")
			if err != nil {
				return err
			}
			if answer != "FORGET "+plan.ID {
				return errPromptCanceled
			}
		}
		result, err := s.ApplyForget(cmd.Context(), plan)
		if jsonOut {
			_ = json.NewEncoder(app.Out).Encode(map[string]any{"result": result, "error": errorText(err)})
		} else {
			fmt.Fprintf(app.Out, "%s · %s\n", result.ID, result.Outcome)
		}
		return err
	}}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview without forgetting metadata")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured preview/result without prompting")
	cmd.Flags().StringVar(&confirm, "confirm-forget", "", "approve forgetting this exact catalog ID")
	return cmd
}
