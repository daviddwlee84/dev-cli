package cli

import (
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshvault"
)

func (w *sshTUIWorkflow) applyVaultKey() error {
	if w.result.Vault != nil {
		return sshvault.ErrPlanUsed
	}
	plan, ok := w.request.Vault.(*sshVaultKeyPlan)
	if !ok || plan == nil {
		return sshvault.ErrStale
	}
	notStarted := sshflow.VaultKeyResult{AttemptID: plan.preview.AttemptID, Status: sshvault.StatusNotStarted, Context: plan.Preview(), AgentStatus: "not_checked"}
	w.result.Vault = &notStarted
	service, err := w.app.sshHosts()
	if err != nil {
		return err
	}
	if service.Paths() != plan.hosts.Paths() {
		return sshvault.ErrStale
	}
	fmt.Fprintln(w.app.Out, "Running the separately approved vault action; no SSH setup is being applied.")
	result, err := applySSHVaultKey(w.ctx, plan)
	public := cloneSSHVaultResult(result)
	w.result.Vault = &public
	w.result.Status = "Vault action: " + result.Status
	// Native output remains public-only even if cancellation stops the dashboard
	// before it can display the completion event.
	fmt.Fprintln(w.app.Out, strings.Join(result.Lines(), "\n"))
	return err
}
