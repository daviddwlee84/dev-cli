package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type sshVaultTestPlan struct{ preview sshflow.VaultKeyPreview }

func (p sshVaultTestPlan) Preview() sshflow.VaultKeyPreview { return p.preview }

func sshVaultTestParent(alias string) sshDialog {
	d := sshDialog{kind: "onboard", onboarding: sshflow.OnboardRequest{Alias: alias, HostName: alias + ".example", User: "tester", Port: 22, Auth: "key", RemoteOS: "posix"}}
	for _, field := range [][3]string{{"alias", "Alias", alias}, {"host", "Host", alias + ".example"}, {"user", "User", "tester"}, {"port", "Port", "22"}, {"auth", "Auth", "key"}, {"key", "Key", ""}, {"os", "OS", "posix"}} {
		d.addField(field[0], field[1], field[2])
	}
	return d
}

func sshVaultTestReceipt(attempt string) *sshflow.VaultKeyReceipt {
	return &sshflow.VaultKeyReceipt{AttemptID: attempt, Observation: 1, Provider: "bitwarden", Title: "Retained item", AccountID: "reviewed-user", VaultID: "personal", ItemID: "item-" + attempt, Fingerprint: "SHA256:test", Status: "created", BindingStatus: "unknown", NativeContextStatus: "observed_consistent", EndpointStatus: "unverified"}
}

func sshVaultTestCompletion(attempt string) sshEventMsg {
	r := sshflow.VaultKeyResult{AttemptID: attempt, Status: "created", AgentStatus: "offered", Receipt: sshVaultTestReceipt(attempt), Keys: []sshflow.VaultAgentKey{{Provider: "bitwarden", Socket: "/fixed.sock", Fingerprint: "SHA256:test"}}}
	return sshEventMsg{kind: "vault-result", vaultAttemptID: attempt, vaultResult: &r}
}

func TestSSHVaultFormRequiresBothApprovalsBeforePreparation(t *testing.T) {
	calls := 0
	var got sshflow.VaultKeyRequest
	m := New(Actions{SSH: SSHActions{
		PrepareVaultKey: func(_ context.Context, r sshflow.VaultKeyRequest) (SSHVaultKeyPlan, error) {
			calls++
			got = r
			return sshVaultTestPlan{sshflow.VaultKeyPreview{AttemptID: "prepared", Request: r, AccountID: "reviewed-user", VaultID: "personal", NativeProfilePath: "/reviewed/profile", NativeEntrypoint: "/reviewed/bw", EndpointStatus: "unverified"}}, nil
		},
		Workflow: func(context.Context, SSHWorkflowRequest) (SSHWorkflow, error) { return &testSSHWorkflow{}, nil },
	}}, nil, nil)
	next, _ := m.openSSHVaultKeyForm(sshVaultTestParent("lab"), "bitwarden-native")
	m = next.(Model)
	if m.sshUI.dialog.value("vaultexperimental") != "no" || m.sshUI.dialog.value("vaultnative") != "no" || calls != 0 {
		t.Fatal("vault picker implicitly approved native context")
	}
	next, cmd := m.prepareSSHVaultKey()
	m = next.(Model)
	if cmd != nil || calls != 0 || m.sshUI.dialog.err == nil {
		t.Fatal("missing approvals reached provider preparation")
	}
	m.sshUI.dialog.setField("vaultexperimental", "yes")
	next, cmd = m.prepareSSHVaultKey()
	m = next.(Model)
	if cmd != nil || calls != 0 {
		t.Fatal("experimental approval replaced native-context approval")
	}
	m.sshUI.dialog.setField("vaultnative", "yes")
	m.sshUI.dialog.setField("vaultaccount", "reviewed-user")
	next, cmd = m.prepareSSHVaultKey()
	m = next.(Model)
	if cmd == nil {
		t.Fatal("explicit approvals did not produce separate review")
	}
	next, _ = m.applySSHEvent(cmd().(sshEventMsg))
	m = next.(Model)
	if calls != 1 || !got.Experimental || !got.NativeContextApproved || m.sshUI.dialog.kind != "vault-review" || !strings.Contains(m.sshUI.dialog.body, "/reviewed/profile") {
		t.Fatal("vault review lost approvals or native context")
	}
	m = sshKeyDialogKey(m, tea.KeyEsc)
	if m.sshUI.dialog.kind != "onboard" || m.sshUI.dialog.value("alias") != "lab" {
		t.Fatal("canceling read-only vault review changed parent")
	}
}

func TestSSHVaultCancelWaitsForActualReceipt(t *testing.T) {
	m := New(Actions{}, nil, nil)
	parent := sshVaultTestParent("lab")
	parent.onboarding.GenerateKey = true
	parent.onboarding.KeyType = sshhost.KeyTypeECDSASK
	parent.onboarding.KeyPath = "/original/handle"
	canceled := false
	m.sshUI.dialog = sshDialog{kind: "vault-running", vaultAttemptID: "A", parent: &parent, vaultCancel: func() { canceled = true }}
	m = sshKeyDialogKey(m, tea.KeyEsc)
	if !canceled || m.sshUI.dialog.kind != "vault-running" {
		t.Fatal("cancel dropped pending mutation")
	}
	next, _ := m.applySSHEvent(sshVaultTestCompletion("A"))
	m = next.(Model)
	if m.sshUI.dialog.kind != "vault-result" || !strings.Contains(m.sshUI.dialog.body, "item-A") {
		t.Fatal("actual receipt not shown")
	}
	m = sshKeyDialogKey(m, tea.KeyEsc)
	if m.sshUI.dialog.onboarding.KeyPath != "/original/handle" || m.sshUI.dialog.onboarding.KeyFingerprint != "" || len(m.sshUI.vaultReceipts) != 1 {
		t.Fatal("cancel selected a key or lost receipt")
	}
	m = sshKeyDialogKey(m, tea.KeyEsc)
	if m.sshUI.dialog.kind != "message" || !strings.Contains(m.sshUI.dialog.body, "item-A") {
		t.Fatal("later SSH cancel lost vault receipt")
	}
}

func TestSSHVaultLateNotStartedWithoutReceiptDoesNotInventLedgerChange(t *testing.T) {
	m := New(Actions{}, nil, nil)
	parent := sshVaultTestParent("B")
	m.sshUI.dialog = sshDialog{kind: "vault-running", vaultAttemptID: "B", parent: &parent}
	m.status = "B is running"
	a := sshflow.VaultKeyResult{AttemptID: "A", Status: "not_started", AgentStatus: "not_checked"}
	next, _ := m.applySSHEvent(sshEventMsg{kind: "vault-result", vaultAttemptID: "A", vaultResult: &a})
	m = next.(Model)
	if m.status != "B is running" || len(m.sshUI.vaultReceipts) != 0 || m.sshUI.dialog.vaultAttemptID != "B" {
		t.Fatal("receipt-less completion invented a receipt or interrupted another action")
	}
}

func TestSSHVaultLateAWhileBRunningDoesNotCancelOrStrandB(t *testing.T) {
	m := New(Actions{}, nil, nil)
	parentB := sshVaultTestParent("B")
	canceledB := false
	m.sshUI.dialog = sshDialog{kind: "vault-running", vaultAttemptID: "B", parent: &parentB, vaultCancel: func() { canceledB = true }}
	m.sshUI.generation = 9
	next, _ := m.applySSHEvent(sshVaultTestCompletion("A"))
	m = next.(Model)
	if canceledB || m.sshUI.dialog.kind != "vault-running" || m.sshUI.dialog.vaultAttemptID != "B" || len(m.sshUI.vaultReceipts) != 1 {
		t.Fatal("late A altered unrelated running B")
	}
	b := sshflow.VaultKeyResult{AttemptID: "B", Status: "not_started", AgentStatus: "not_checked"}
	next, _ = m.applySSHEvent(sshEventMsg{kind: "vault-result", generation: 1, vaultAttemptID: "B", vaultResult: &b})
	m = next.(Model)
	if m.sshUI.dialog.kind != "vault-result" || m.sshUI.dialog.vaultResult.Status != "not_started" || !strings.Contains(m.sshUI.dialog.body, "item-A") {
		t.Fatal("receipt-less B completion was discarded or lost A")
	}
	m = sshKeyDialogKey(m, tea.KeyEnter)
	if m.sshUI.dialog.kind != "onboard" || m.sshUI.dialog.value("alias") != "B" {
		t.Fatal("acknowledgment restored a finished running dialog")
	}
	next, _ = m.applySSHEvent(sshVaultTestCompletion("A"))
	m = next.(Model)
	if m.sshUI.dialog.value("alias") != "B" || len(m.sshUI.vaultReceipts) != 1 {
		t.Fatal("duplicate completion changed fields or duplicated receipt")
	}
}

func TestSSHVaultLateReceiptSurvivesOtherVaultFormCancelAndApply(t *testing.T) {
	for _, apply := range []bool{false, true} {
		m := New(Actions{SSH: SSHActions{Workflow: func(context.Context, SSHWorkflowRequest) (SSHWorkflow, error) { return &testSSHWorkflow{}, nil }}}, nil, nil)
		parent := sshVaultTestParent("B")
		kind := "vault-form"
		if apply {
			kind = "vault-review"
		}
		plan := sshVaultTestPlan{sshflow.VaultKeyPreview{AttemptID: "B", Request: sshflow.VaultKeyRequest{Provider: "1password", Title: "B"}}}
		m.sshUI.dialog = sshDialog{kind: kind, parent: &parent, vaultPlan: plan}
		next, _ := m.applySSHEvent(sshVaultTestCompletion("A"))
		m = next.(Model)
		if m.sshUI.dialog.kind != kind {
			t.Fatal("late receipt replaced other vault form/review")
		}
		if apply {
			next, cmd := m.applySSHVaultKey()
			m = next.(Model)
			if cmd == nil || m.sshUI.dialog.kind != "vault-running" {
				t.Fatal("B review could not apply")
			}
			if m.sshUI.dialog.vaultCancel != nil {
				m.sshUI.dialog.vaultCancel()
			}
			b := sshflow.VaultKeyResult{AttemptID: "B", Status: "not_started", AgentStatus: "not_checked"}
			next, _ = m.applySSHEvent(sshEventMsg{kind: "vault-result", vaultAttemptID: "B", vaultResult: &b})
			m = next.(Model)
			if !strings.Contains(m.sshUI.dialog.body, "item-A") {
				t.Fatal("B result lost A receipt")
			}
			m = sshKeyDialogKey(m, tea.KeyEnter)
		} else {
			m = sshKeyDialogKey(m, tea.KeyEsc)
		}
		if m.sshUI.dialog.value("alias") != "B" || len(m.sshUI.vaultReceipts) != 1 {
			t.Fatal("B completion/cancel lost owning ledger")
		}
		m = sshKeyDialogKey(m, tea.KeyEsc)
		if !strings.Contains(m.sshUI.dialog.body, "item-A") {
			t.Fatal("later SSH cancel omitted ledger receipt")
		}
	}
}

func TestSSHVaultLateReceiptAfterImmutableSSHReviewReachesCompletion(t *testing.T) {
	m := New(Actions{}, nil, nil)
	plan := &testSSHOnboardPlan{preview: sshflow.OnboardPreview{}}
	m.sshUI.dialog = sshVaultTestParent("lab")
	m.sshUI.dialog.kind, m.sshUI.dialog.plan = "review", plan
	next, _ := m.applySSHEvent(sshVaultTestCompletion("A"))
	m = next.(Model)
	if m.sshUI.dialog.kind != "review" || m.sshUI.dialog.plan != plan || len(plan.Preview().Notes) != 0 {
		t.Fatal("late receipt mutated/restored immutable SSH review")
	}
	result := &sshflow.OnboardExecutionResult{OnboardResult: sshflow.OnboardResult{Status: "ready"}}
	m.finishSSHOnboarding(SSHWorkflowResult{Onboarding: result})
	if len(result.VaultReceipts) != 1 || !strings.Contains(m.sshUI.dialog.body, "item-A") {
		t.Fatal("SSH completion omitted late model-ledger receipt")
	}
}

func TestSSHVaultMissingLateOutcomeIsRetainedWithoutInterruptingAnotherAttempt(t *testing.T) {
	m := New(Actions{}, nil, nil)
	parent := sshVaultTestParent("B")
	canceledB := false
	m.sshUI.dialog = sshDialog{kind: "vault-running", vaultAttemptID: "B", parent: &parent, vaultCancel: func() { canceledB = true }}
	plan := sshVaultTestPlan{sshflow.VaultKeyPreview{AttemptID: "A", Request: sshflow.VaultKeyRequest{Provider: "bitwarden", Title: "Unknown attempt", Experimental: true, NativeContextApproved: true}, EndpointStatus: "unverified"}}
	next, _ := m.applySSHEvent(sshEventMsg{kind: "vault-result", vaultAttemptID: "A", vaultPlan: plan})
	m = next.(Model)
	if canceledB || m.sshUI.dialog.vaultAttemptID != "B" || len(m.sshUI.vaultReceipts) != 1 || m.sshUI.vaultReceipts[0].Status != "unknown" || m.sshUI.vaultReceipts[0].ItemID != "" {
		t.Fatal("missing late outcome was dropped, invented an item or interrupted B")
	}
	b := sshflow.VaultKeyResult{AttemptID: "B", Status: "not_started", AgentStatus: "not_checked"}
	next, _ = m.applySSHEvent(sshEventMsg{kind: "vault-result", vaultAttemptID: "B", vaultResult: &b})
	m = next.(Model)
	if !strings.Contains(m.sshUI.dialog.body, "Unknown attempt") || !strings.Contains(m.sshUI.dialog.body, "No item ID") {
		t.Fatal("completion report omitted prior unknown attempt")
	}
}

func TestSSHVaultReceiptMergeTracksAttemptsAndObservationOrder(t *testing.T) {
	a := sshflow.VaultKeyReceipt{AttemptID: "A", Observation: 1, Provider: "1password", Title: "same", AccountID: "same", VaultID: "same", Status: "unknown", BindingStatus: "unknown"}
	b := a
	b.AttemptID = "B"
	ledger := sshflow.RetainVaultKeyReceipt(nil, &a)
	ledger = sshflow.RetainVaultKeyReceipt(ledger, &b)
	ledger = sshflow.RetainVaultKeyReceipt(ledger, &a)
	if len(ledger) != 2 {
		t.Fatal("distinct same-shaped attempts collapsed or duplicate delivery was added")
	}
	created := a
	created.Observation = 2
	created.Status = "created"
	created.ItemID = "known-item"
	created.Fingerprint = "SHA256:known"
	created.BindingStatus = "verified"
	ledger = sshflow.RetainVaultKeyReceipt(ledger, &created)
	uncertain := a
	uncertain.Observation = 3
	uncertain.NativeContextStatus = "unknown"
	ledger = sshflow.RetainVaultKeyReceipt(ledger, &uncertain)
	ledger = sshflow.RetainVaultKeyReceipt(ledger, &created)
	if ledger[0].Status != "created" || ledger[0].ItemID != "known-item" || ledger[0].Fingerprint != "SHA256:known" || ledger[0].BindingStatus != "unknown" {
		t.Fatal("late consistent observation erased newer uncertainty or known existence")
	}
	conflict := created
	conflict.Observation = 4
	conflict.ItemID = "other-item"
	ledger = sshflow.RetainVaultKeyReceipt(ledger, &conflict)
	if ledger[0].ItemID != "known-item" || len(ledger[0].ConflictingIdentities) != 1 || ledger[0].ConflictingIdentities[0].ItemID != "other-item" {
		t.Fatal("conflicting nonempty identity was silently overwritten or lost")
	}
}

func TestSSHDesktopSingleFingerprintRequiresExplicitChoiceWithoutItemReceipt(t *testing.T) {
	m := New(Actions{}, nil, nil)
	parent := sshVaultTestParent("lab")
	m.sshUI.dialog = sshDialog{kind: "vault-running", vaultAttemptID: "desktop", parent: &parent}
	result := sshflow.VaultKeyResult{AttemptID: "desktop", Status: "observed", AgentStatus: "offered", Keys: []sshflow.VaultAgentKey{{Provider: "bitwarden", Socket: "/fixed.sock", Fingerprint: "SHA256:visible", Algorithm: "ssh-ed25519"}}}
	next, _ := m.applySSHEvent(sshEventMsg{kind: "vault-result", vaultAttemptID: "desktop", vaultResult: &result})
	m = next.(Model)
	m = sshKeyDialogKey(m, tea.KeyEnter)
	if m.sshUI.dialog.kind != "vault-keys" || len(m.sshUI.dialog.options) != 2 || m.sshUI.dialog.parent.onboarding.KeyFingerprint != "" {
		t.Fatal("single newly visible key was automatically selected")
	}
	m = sshKeyDialogKey(m, tea.KeyEnter)
	if m.sshUI.dialog.onboarding.KeyFingerprint != "SHA256:visible" || len(m.sshUI.vaultReceipts) != 0 {
		t.Fatal("desktop choice failed or fabricated item receipt")
	}
}
