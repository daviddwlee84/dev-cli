package cli

import (
	"context"
	"errors"

	"github.com/daviddwlee84/dev-cli/internal/picker"
)

// sshRegistrationDestinations is optional selection: accepting no checked
// destinations means no registration, whereas Escape cancels the workflow.
// Keep sshPick's required-selection contract for host and source pickers.
func sshRegistrationDestinations(ctx context.Context, app *App, prompt string) (string, error) {
	result, attempted, err := app.pick(ctx, picker.Request{
		Prompt: prompt,
		Items: []picker.Item{
			{Value: "fleet", Label: "Fleet", Description: "Optional repository and task inventory"},
			{Value: "herdr", Label: "Herdr", Description: "Optional native machine registration"},
		},
		Multi: true,
	})
	if errors.Is(err, picker.ErrCanceled) {
		return "", errPromptCanceled
	}
	if err != nil {
		return "", err
	}
	if !attempted {
		return "", errors.New("this wizard requires an interactive terminal; use explicit command flags")
	}
	if result.Item.Value != "" {
		return "", errors.New("registration picker returned a single-choice result")
	}
	selected := map[string]bool{}
	for _, item := range result.Items {
		if item.Value != "fleet" && item.Value != "herdr" {
			return "", errors.New("registration picker returned an unknown destination")
		}
		if selected[item.Value] {
			return "", errors.New("registration picker returned a duplicate destination")
		}
		selected[item.Value] = true
	}
	switch {
	case selected["fleet"] && selected["herdr"]:
		return "both", nil
	case selected["fleet"]:
		return "fleet", nil
	case selected["herdr"]:
		return "herdr", nil
	default:
		return "", nil
	}
}
