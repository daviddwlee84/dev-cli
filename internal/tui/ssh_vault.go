package tui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

func (m Model) openSSHVaultKeyForm(parent sshDialog, action string) (tea.Model, tea.Cmd) {
	if m.actions.SSH.PrepareVaultKey == nil || m.actions.SSH.Workflow == nil {
		parent.err = errors.New("Vault-key creation is unavailable in this dashboard")
		m.sshUI.dialog = parent
		return m, nil
	}
	m.pauseSSHBackground()
	m.mergeSSHVaultReceipts(parent.onboarding.VaultReceipts)
	alias := parent.value("alias")
	if parent.onboarding.Profile != nil {
		alias = parent.onboarding.Profile.Alias
	}
	request := sshflow.VaultKeyRequest{Title: "SSH key for " + alias}
	switch action {
	case "1password":
		request.Provider = "1password"
	case "bitwarden-native":
		request.Provider, request.VaultID = "bitwarden", "personal"
	case "bitwarden-desktop":
		request.Provider, request.Desktop, request.Title = "bitwarden", true, "Bitwarden desktop handoff"
	default:
		parent.err = errors.New("Choose a supported vault action")
		m.sshUI.dialog = parent
		return m, nil
	}
	form := sshDialog{kind: "vault-form", title: "Separate vault key creation", parent: &parent, vaultRequest: request}
	if !request.Desktop {
		form.addField("vaultaccount", "Account ID", "")
		form.addField("vaultid", "Vault ID", request.VaultID)
		form.addField("vaulttitle", "Item title", request.Title)
		if request.Provider == "bitwarden" {
			form.addField("vaultexperimental", "Experimental", "no")
			form.addField("vaultnative", "Native context", "no")
		}
	}
	m.sshUI.dialog = form
	if request.Desktop {
		return m.prepareSSHVaultKey()
	}
	return m, m.focusSSHField(0)
}

func (m Model) prepareSSHVaultKey() (tea.Model, tea.Cmd) {
	d := &m.sshUI.dialog
	r := d.vaultRequest
	if !r.Desktop {
		r.AccountID, r.VaultID = strings.TrimSpace(d.value("vaultaccount")), strings.TrimSpace(d.value("vaultid"))
		r.Title = strings.TrimSpace(d.value("vaulttitle"))
		r.Experimental, r.NativeContextApproved = d.value("vaultexperimental") == "yes", d.value("vaultnative") == "yes"
		if r.Provider == "bitwarden" && (!r.Experimental || !r.NativeContextApproved) {
			d.err = errors.New("Bitwarden requires two separate approvals: experimental in-memory generation and native-profile endpoint delegation. Endpoints remain unverified")
			return m, nil
		}
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	d.vaultRequest, d.vaultCancel, d.kind, d.err = r, cancel, "vault-preparing", nil
	d.body = "Reading the explicitly selected native vault context or captured agent baseline; no item is being created."
	m.sshUI.generation++
	generation := m.sshUI.generation
	prepare := m.actions.SSH.PrepareVaultKey
	return m, func() tea.Msg {
		defer cancel()
		plan, err := prepare(ctx, r)
		return sshEventMsg{kind: "vault-prepared", generation: generation, vaultPlan: plan, err: err}
	}
}

func (m Model) finishSSHVaultPreparation(msg sshEventMsg) (tea.Model, tea.Cmd) {
	d := &m.sshUI.dialog
	if d.kind != "vault-preparing" {
		return m, nil
	}
	d.vaultCancel = nil
	if msg.err != nil || msg.vaultPlan == nil {
		d.err = msg.err
		if d.err == nil {
			d.err = errors.New("Vault preparation returned no reviewed plan")
		}
		if d.vaultRequest.Desktop {
			d.kind, d.body = "vault-result", "No complete Bitwarden agent baseline was obtained; no vault item was created by dev."
			return m, nil
		}
		d.kind = "vault-form"
		return m, m.focusSSHField(d.index)
	}
	d.vaultPlan, d.kind = msg.vaultPlan, "vault-review"
	d.body = strings.Join(msg.vaultPlan.Preview().Lines(), "\n")
	return m, nil
}

func (m Model) applySSHVaultKey() (tea.Model, tea.Cmd) {
	d := m.sshUI.dialog
	if d.kind != "vault-review" || d.vaultPlan == nil {
		return m, nil
	}
	attemptID := d.vaultPlan.Preview().AttemptID
	if attemptID == "" || m.sshUI.vaultCompleted[attemptID] {
		m.sshUI.dialog.err = errors.New("Prepare a new vault action; this reviewed attempt is missing or already completed")
		return m, nil
	}
	ctx, cancel := context.WithCancel(m.baseContext())
	workflow, err := m.actions.SSH.Workflow(ctx, SSHWorkflowRequest{Action: "vault-key", Vault: d.vaultPlan})
	if err != nil {
		cancel()
		m.sshUI.dialog.err = err
		return m, nil
	}
	m.sshUI.generation++
	generation := m.sshUI.generation
	parent := d.parent
	m.sshUI.dialog = sshDialog{kind: "vault-running", title: "Vault key action running", body: "Cancellation waits for the actual creation receipt; no SSH setup is being applied.", parent: parent, vaultPlan: d.vaultPlan, vaultAttemptID: attemptID, vaultCancel: cancel}
	return m, tea.Exec(workflow, func(err error) tea.Msg {
		cancel()
		return afterExec(sshEventMsg{kind: "vault-result", generation: generation, vaultAttemptID: attemptID, vaultPlan: d.vaultPlan, vaultResult: workflow.Result().Vault, vaultParent: parent, err: err})
	})
}

func copySSHVaultResult(result sshflow.VaultKeyResult) sshflow.VaultKeyResult {
	result.Keys = append([]sshflow.VaultAgentKey(nil), result.Keys...)
	result.Notes = append([]string(nil), result.Notes...)
	result.Context.Notes = append([]string(nil), result.Context.Notes...)
	if result.Receipt != nil {
		receipt := sshflow.CloneVaultKeyReceipt(*result.Receipt)
		result.Receipt = &receipt
	}
	return result
}

func (m Model) finishSSHVaultResult(msg sshEventMsg) (tea.Model, tea.Cmd) {
	attemptID := msg.vaultAttemptID
	if attemptID == "" && msg.vaultPlan != nil {
		attemptID = msg.vaultPlan.Preview().AttemptID
	}
	if attemptID == "" && msg.vaultResult != nil {
		attemptID = msg.vaultResult.AttemptID
	}
	if attemptID == "" && msg.vaultResult != nil && msg.vaultResult.Receipt != nil {
		attemptID = msg.vaultResult.Receipt.AttemptID
	}
	if msg.vaultResult == nil {
		preview := sshflow.VaultKeyPreview{}
		if msg.vaultPlan != nil {
			preview = msg.vaultPlan.Preview()
		}
		unknown := sshflow.VaultKeyResult{AttemptID: attemptID, Status: "unknown", Context: preview, AgentStatus: "not_checked", Notes: []string{"The started workflow returned no outcome. Inspect the native vault before another creation attempt; do not retry automatically."}}
		if !preview.Request.Desktop {
			unknown.Receipt = &sshflow.VaultKeyReceipt{AttemptID: attemptID, Observation: sshflow.NextVaultObservation(), Provider: preview.Request.Provider, Title: preview.Request.Title, AccountID: preview.AccountID, VaultID: preview.VaultID, Status: "unknown", BindingStatus: "unknown", NativeContextStatus: "unknown", EndpointStatus: preview.EndpointStatus, ProfilePath: preview.NativeProfilePath}
		}
		msg.vaultResult = &unknown
	}
	result := copySSHVaultResult(*msg.vaultResult)
	result.AttemptID = attemptID
	if result.Receipt != nil {
		result.Receipt.AttemptID = attemptID
	}
	before := m.sshUI.vaultReceipts
	m.sshUI.vaultReceipts = sshflow.RetainVaultKeyReceipt(before, result.Receipt)
	changed := !reflect.DeepEqual(before, m.sshUI.vaultReceipts)
	completed := m.sshUI.vaultCompleted[attemptID]
	active := attemptID != "" && !completed && m.sshUI.dialog.kind == "vault-running" && m.sshUI.dialog.vaultAttemptID == attemptID
	if attemptID != "" {
		if m.sshUI.vaultCompleted == nil {
			m.sshUI.vaultCompleted = map[string]bool{}
		}
		m.sshUI.vaultCompleted[attemptID] = true
	}
	if !active {
		// A completion from A must never cancel, replace or restore B's dialog.
		// Late receipts are model-owned facts only, never fresh key selection.
		if changed {
			m.status = "Vault receipt retained for attempt " + attemptID + "; current action and SSH fields are unchanged"
			if (m.sshUI.dialog.kind == "vault-result" || m.sshUI.dialog.kind == "vault-keys") && m.sshUI.dialog.vaultAttemptID == attemptID && m.sshUI.dialog.vaultResult != nil {
				m.sshUI.dialog.vaultResult.Keys = nil
				m.sshUI.dialog.vaultResult.AgentStatus = "not_selected"
				m.sshUI.dialog.options = nil
				m.sshUI.dialog.kind = "vault-result"
			}
			m.refreshSSHVaultReceiptBody()
		}
		return m, nil
	}
	parent := m.sshUI.dialog.parent
	if result.Status != "created" && result.Status != "observed" {
		result.Keys = nil
	}
	m.sshUI.generation++
	m.sshUI.dialog = sshDialog{kind: "vault-result", title: "Vault action receipt — separate from SSH setup", err: msg.err, parent: parent, vaultAttemptID: attemptID, vaultResult: &result}
	m.refreshSSHVaultReceiptBody()
	m.status = "Vault action result retained; no SSH setup was applied"
	return m, nil
}

func (m *Model) mergeSSHVaultReceipts(receipts []sshflow.VaultKeyReceipt) []sshflow.VaultKeyReceipt {
	for _, receipt := range receipts {
		m.sshUI.vaultReceipts = sshflow.RetainVaultKeyReceipt(m.sshUI.vaultReceipts, &receipt)
	}
	return sshflow.CloneVaultKeyReceipts(m.sshUI.vaultReceipts)
}

func (m *Model) refreshSSHVaultReceiptBody() {
	d := &m.sshUI.dialog
	if d.kind != "vault-result" || d.vaultResult == nil {
		return
	}
	for _, receipt := range m.sshUI.vaultReceipts {
		if receipt.AttemptID != "" && receipt.AttemptID == d.vaultAttemptID {
			copy := sshflow.CloneVaultKeyReceipt(receipt)
			d.vaultResult.Receipt = &copy
			d.vaultResult.BindingStatus, d.vaultResult.NativeContextStatus, d.vaultResult.EndpointStatus = copy.BindingStatus, copy.NativeContextStatus, copy.EndpointStatus
			if copy.Status == "created" || copy.Status == "unknown" {
				d.vaultResult.Status = copy.Status
			}
			break
		}
	}
	lines := d.vaultResult.Lines()
	for _, receipt := range m.sshUI.vaultReceipts {
		if receipt.AttemptID != "" && receipt.AttemptID == d.vaultAttemptID {
			continue
		}
		lines = append(lines, "Other retained vault item/attempt:")
		lines = append(lines, receipt.Lines()...)
	}
	d.body = strings.Join(lines, "\n")
}

func (m Model) acknowledgeSSHVaultResult() (tea.Model, tea.Cmd) {
	d := &m.sshUI.dialog
	if d.vaultResult == nil || len(d.vaultResult.Keys) == 0 {
		return m.returnSSHVaultResult(-1)
	}
	d.kind, d.index, d.options, d.body = "vault-keys", 0, nil, ""
	for _, key := range d.vaultResult.Keys {
		d.options = append(d.options, key.Fingerprint+" · "+key.Algorithm+" · "+key.Comment)
	}
	d.options = append(d.options, "Return without selecting a key (vault effects retained)")
	return m, nil
}

func (m Model) returnSSHVaultResult(index int) (tea.Model, tea.Cmd) {
	d := m.sshUI.dialog
	if d.parent == nil {
		m.sshUI.dialog = sshDialog{}
		return m, m.scheduleSSHBackground()
	}
	parent := *d.parent
	if result := d.vaultResult; result != nil {
		m.sshUI.vaultReceipts = sshflow.RetainVaultKeyReceipt(m.sshUI.vaultReceipts, result.Receipt)
		if index >= 0 && index < len(result.Keys) {
			key := result.Keys[index]
			parent.applyAgentKeyChoice(SSHKeyChoice{Label: key.Provider + " agent: " + key.Fingerprint, Fingerprint: key.Fingerprint, AgentProvider: key.Provider, AgentSocket: key.Socket})
		}
		if result.Receipt != nil {
			m.status = "Vault item/attempt retained: " + result.Receipt.Provider + " " + result.Receipt.ItemID
		}
	}
	parent.onboarding.VaultReceipts = m.mergeSSHVaultReceipts(parent.onboarding.VaultReceipts)
	m.sshUI.generation++
	m.sshUI.dialog = parent
	return m, m.focusSSHField(parent.index)
}

func (m Model) cancelSSHVaultDialog() (tea.Model, tea.Cmd) {
	d := &m.sshUI.dialog
	if d.kind == "vault-running" {
		if d.vaultCancel != nil {
			d.vaultCancel()
		}
		d.body = "Cancellation requested; waiting for the actual vault receipt/unknown outcome. Completed native effects are retained."
		return m, nil
	}
	if d.kind == "vault-result" || d.kind == "vault-keys" {
		return m.returnSSHVaultResult(-1)
	}
	if d.vaultCancel != nil {
		d.vaultCancel()
	}
	if d.parent != nil {
		parent := *d.parent
		parent.onboarding.VaultReceipts = m.mergeSSHVaultReceipts(parent.onboarding.VaultReceipts)
		m.sshUI.generation++
		m.sshUI.dialog = parent
		return m, m.focusSSHField(parent.index)
	}
	m.sshUI.dialog = sshDialog{}
	return m, nil
}

func (m Model) cancelSSHFormWithVaultReceipts() (tea.Model, tea.Cmd) {
	var lines []string
	for _, receipt := range m.mergeSSHVaultReceipts(m.sshUI.dialog.onboarding.VaultReceipts) {
		lines = append(lines, receipt.Lines()...)
	}
	m.sshUI.generation++
	m.sshUI.dialog = sshDialog{kind: "message", title: "SSH setup canceled — vault items/attempts retained", body: strings.Join(lines, "\n")}
	m.status = fmt.Sprintf("SSH setup canceled; %d vault receipt lines retained for review", len(lines))
	return m, nil
}
