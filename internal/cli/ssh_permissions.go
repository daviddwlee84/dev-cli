package cli

import (
	"context"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

const sshPermissionsConfirmation = "Apply these SSH permission repairs and continue?"

// This is a separately reviewed local operation before the remaining wizard.
// Completed tightening remains in place if onboarding is canceled later.
func preflightSSHWizardPermissions(ctx context.Context, app *App, service *sshhost.Service, keyPath string) error {
	plan, err := service.PlanPermissions(ctx, sshhost.PermissionRequest{KeyPath: keyPath})
	if err != nil {
		return err
	}
	if !plan.Ready() {
		renderSSHDiagnostics(app, plan.Diagnostics)
		return fmt.Errorf("SSH permissions require manual attention before setup: %w", sshhost.ErrUnsafePath)
	}
	if !plan.NeedsRepair() {
		return nil
	}
	fmt.Fprintln(app.Out, "SSH permission repairs:")
	renderSSHPermissionChanges(app, plan)
	fmt.Fprintln(app.Out, "Completed repairs remain in place if you cancel setup later.")
	confirmed, err := newPrompter(app).confirm(sshPermissionsConfirmation, false)
	if err != nil {
		return err
	}
	if !confirmed {
		return errPromptCanceled
	}
	result, err := service.ApplyPermissions(ctx, plan)
	renderSSHPermissionOutcomes(app, result)
	if err != nil {
		return fmt.Errorf("SSH permission repair %s: %w", result.Status, err)
	}
	return nil
}

func renderSSHPermissionChanges(app *App, plan sshhost.PermissionPlan) {
	for _, change := range plan.Changes {
		fmt.Fprintf(app.Out, "  %s: %04o → %04o\n", change.Path, change.BeforeMode.Perm(), change.AfterMode.Perm())
	}
}

func renderSSHPermissionOutcomes(app *App, result sshhost.PermissionResult) {
	for _, outcome := range result.Outcomes {
		fmt.Fprintf(app.Out, "  %s: %s (%04o → %04o)\n", outcome.Path, outcome.Status, outcome.BeforeMode.Perm(), outcome.AfterMode.Perm())
	}
}
