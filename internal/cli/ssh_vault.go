package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync/atomic"

	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshvault"
)

type sshVaultBackend interface {
	Plan(context.Context, sshvault.Request) (sshvault.Plan, error)
	Apply(context.Context, sshvault.Plan) (sshvault.Result, error)
}

type sshVaultBackendKey struct{}

func sshVaultBackendFor(ctx context.Context) sshVaultBackend {
	if backend, ok := ctx.Value(sshVaultBackendKey{}).(sshVaultBackend); ok {
		return backend
	}
	return sshvault.NewService(nil)
}

type sshVaultKeyPlan struct {
	backend  sshVaultBackend
	plan     sshvault.Plan
	request  sshflow.VaultKeyRequest
	preview  sshflow.VaultKeyPreview
	hosts    *sshhost.Service
	baseline *sshhost.AgentInventory
	used     atomic.Bool
}

func (p *sshVaultKeyPlan) Preview() sshflow.VaultKeyPreview {
	return cloneSSHVaultPreview(p.preview)
}

func cloneSSHVaultPreview(preview sshflow.VaultKeyPreview) sshflow.VaultKeyPreview {
	preview.Notes = append([]string(nil), preview.Notes...)
	return preview
}

func cloneSSHVaultResult(result sshflow.VaultKeyResult) sshflow.VaultKeyResult {
	result.Context = cloneSSHVaultPreview(result.Context)
	result.Keys = append([]sshflow.VaultAgentKey(nil), result.Keys...)
	result.Notes = append([]string(nil), result.Notes...)
	if result.Receipt != nil {
		receipt := sshflow.CloneVaultKeyReceipt(*result.Receipt)
		result.Receipt = &receipt
	}
	return result
}

func normalizeSSHVaultRequest(request sshflow.VaultKeyRequest) (sshflow.VaultKeyRequest, sshvault.Request, error) {
	if request.Desktop {
		if request.Provider != string(sshvault.Bitwarden) || request.AccountID != "" || request.VaultID != "" || request.Experimental || request.NativeContextApproved {
			return request, sshvault.Request{}, errors.New("Bitwarden desktop handoff cannot bind an account/vault or use native-context/experimental creation flags")
		}
		request.Title = "Bitwarden desktop handoff"
		return request, sshvault.Request{}, nil
	}
	if request.Provider == string(sshvault.Bitwarden) && request.VaultID == "" {
		request.VaultID = sshvault.PersonalVault
	}
	native := sshvault.Request{Provider: sshvault.Provider(request.Provider), AccountID: request.AccountID, VaultID: request.VaultID, Title: request.Title, Experimental: request.Experimental, NativeContextApproved: request.NativeContextApproved}
	if err := sshvault.ValidateRequest(native); err != nil {
		return request, native, err
	}
	if native.Provider == sshvault.Bitwarden && !native.NativeContextApproved {
		return request, native, sshvault.ErrUnsupportedContext
	}
	return request, native, nil
}

func prepareSSHVaultKey(ctx context.Context, app *App, request sshflow.VaultKeyRequest) (*sshVaultKeyPlan, error) {
	request, native, err := normalizeSSHVaultRequest(request)
	if err != nil {
		return nil, publicSSHVaultError(err)
	}
	hosts, err := app.sshHosts()
	if err != nil {
		return nil, err
	}
	prepared := &sshVaultKeyPlan{request: request, hosts: hosts}
	if !request.Desktop {
		prepared.backend = sshVaultBackendFor(ctx)
		prepared.plan, err = prepared.backend.Plan(ctx, native)
		if err != nil {
			return nil, publicSSHVaultError(err)
		}
		prepared.preview = sshVaultPlanPreview(request, prepared.plan)
	} else {
		prepared.preview = sshflow.VaultKeyPreview{Request: request, Notes: []string{
			"Baseline inventory is from one complete captured Bitwarden SSH agent, not from a vault query.",
			"In Bitwarden desktop, create or unlock the desired SSH key and enable its SSH agent; dev does not automate the GUI.",
			"Continue only after those native steps. The same socket will be refreshed; newly visible keys do not prove newly created items.",
		}}
	}
	ref, err := hosts.ResolveAgentSocket(runtime.GOOS, request.Provider)
	if err == nil {
		inventory, inventoryErr := hosts.ObserveAgentKeys(ctx, ref)
		if inventoryErr == nil && inventory.Complete {
			prepared.baseline = &inventory
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prepared.baseline != nil {
		prepared.preview.Notes = append(prepared.preview.Notes, fmt.Sprintf("Captured SSH agent: %s (%d baseline identities; listing is not signing proof).", prepared.baseline.Agent.Socket, len(prepared.baseline.Candidates)))
	}
	if prepared.baseline == nil {
		if request.Desktop {
			return nil, errors.New("Bitwarden desktop handoff requires a complete reachable agent baseline; enable/unlock its SSH agent and reopen the picker")
		}
		prepared.preview.Notes = append(prepared.preview.Notes, "The provider agent baseline is unavailable. Creation can proceed, but only a retained receipt will be returned; enable/unlock the agent and refresh the key picker later.")
	}
	var attempt [16]byte
	if _, err := rand.Read(attempt[:]); err != nil {
		return nil, sshvault.ErrUnavailable
	}
	prepared.preview.AttemptID = hex.EncodeToString(attempt[:])
	return prepared, nil
}

func sshVaultPlanPreview(request sshflow.VaultKeyRequest, plan sshvault.Plan) sshflow.VaultKeyPreview {
	preview := sshflow.VaultKeyPreview{Request: request, AccountID: plan.Destination.AccountID, UserID: plan.Destination.UserID, VaultID: plan.Destination.VaultID, VaultName: plan.Destination.VaultName, AccountLabel: plan.Destination.AccountLabel, ServerLabel: plan.Destination.ServerURL, Version: plan.Version, EndpointStatus: plan.EndpointStatus}
	tool := "op"
	if request.Provider == string(sshvault.Bitwarden) {
		tool = "bw"
	}
	preview.ToolPath, _ = exec.LookPath(tool) // An observation only; the vault domain owns native execution binding.
	if profile := plan.NativeProfile; profile != nil {
		preview.NativeProfilePath, preview.NativeEntrypoint, preview.NativeRuntime = profile.Path, profile.Entrypoint, profile.Runtime
		preview.NativePackageRoot, preview.NativeCWD = profile.PackageRoot, profile.CWD
		preview.ToolPath = profile.Entrypoint
	}
	preview.Notes = []string{"Creation is separate from SSH setup: no SSH configuration, remote key installation, Fleet or Herdr registration is performed.", "The vault item is retained if later SSH setup is canceled or fails; dev never automatically retries creation or deletes an item."}
	if request.Provider == string(sshvault.Bitwarden) {
		preview.Notes = append(preview.Notes,
			"Experimental: the private key briefly exists in dev process memory and native CLI input. Dev writes no plaintext private-key file; OS swap/core dumps, native-provider persistence and perfect memory erasure are not guaranteed.",
			"Native-context approval delegates endpoint/configuration authority to the displayed Bitwarden profile. Its server label is advisory, not endpoint attestation.",
			"Bitwarden destination binding remains unknown and endpoints remain unverified, including after a created response with consistent native observations.")
	} else {
		preview.Notes = append(preview.Notes, "1Password performs generation; dev does not save private key files or expose private fields.")
	}
	return preview
}

func applySSHVaultKey(ctx context.Context, prepared *sshVaultKeyPlan) (sshflow.VaultKeyResult, error) {
	if prepared == nil {
		return sshflow.VaultKeyResult{Status: sshvault.StatusNotStarted, AgentStatus: "not_requested"}, sshvault.ErrStale
	}
	result := sshflow.VaultKeyResult{AttemptID: prepared.preview.AttemptID, Status: sshvault.StatusNotStarted, Context: prepared.Preview(), AgentStatus: "not_checked"}
	if prepared.used.Swap(true) {
		return result, sshvault.ErrPlanUsed
	}
	if prepared.request.Desktop {
		return applySSHBitwardenHandoff(ctx, prepared, result)
	}
	native, applyErr := prepared.backend.Apply(ctx, prepared.plan)
	result.Status, result.BindingStatus = native.Status, native.BindingStatus
	result.NativeContextStatus, result.EndpointStatus = native.NativeContextStatus, native.EndpointStatus
	if native.Status == sshvault.StatusCreated || native.Status == sshvault.StatusUnknown {
		receipt := sshflow.VaultKeyReceipt{AttemptID: prepared.preview.AttemptID, Observation: sshflow.NextVaultObservation(), Provider: prepared.request.Provider, Title: prepared.request.Title, AccountID: prepared.plan.Destination.AccountID, VaultID: prepared.plan.Destination.VaultID, ProfilePath: prepared.preview.NativeProfilePath, Status: native.Status, BindingStatus: native.BindingStatus, NativeContextStatus: native.NativeContextStatus, EndpointStatus: native.EndpointStatus}
		if observed := native.Receipt; observed != nil {
			receipt.Provider, receipt.AccountID, receipt.VaultID = string(observed.Provider), observed.AccountID, observed.VaultID
			receipt.ItemID, receipt.Fingerprint, receipt.ProfilePath = observed.ItemID, observed.Fingerprint, observed.ProfilePath
		}
		result.Receipt = &receipt
		result.Notes = append(result.Notes, "Retain this receipt even if SSH setup is canceled. No vault item is automatically retried or deleted.")
	}
	if applyErr != nil {
		result.Notes = append(result.Notes, "Creation or its destination is incomplete/unknown; inspect the native vault before another creation attempt. No agent key was selected.")
		return result, publicSSHVaultError(applyErr)
	}
	if !sshvault.CanSelectAgentKey(prepared.plan, native) {
		result.AgentStatus = "not_selected"
		result.Notes = append(result.Notes, "The result does not authorize a created-key agent match; retain its metadata and reconcile the vault.")
		return result, nil
	}
	if prepared.baseline == nil {
		result.AgentStatus = "unavailable"
		result.Notes = append(result.Notes, "The item was created, but no provider agent was captured for this operation. Enable/unlock the provider agent and select the receipt fingerprint from a refreshed key picker.")
		return result, nil
	}
	inventory, err := prepared.hosts.RefreshAgentKeys(ctx, *prepared.baseline)
	if err != nil || !inventory.Complete {
		result.AgentStatus = "unavailable"
		result.Notes = append(result.Notes, "The vault item is retained, but the captured provider agent changed or could not be completely inspected. Refresh the key picker; do not create another item automatically.")
		return result, nil
	}
	metadata, err := sshhost.ParsePublicKey([]byte(native.Receipt.PublicLine))
	if err != nil || metadata.Fingerprint != native.Receipt.Fingerprint {
		result.AgentStatus = "not_selected"
		result.Notes = append(result.Notes, "Created public-key metadata did not match the receipt; no agent key was selected.")
		return result, nil
	}
	for _, key := range inventory.Candidates {
		if key.Fingerprint == metadata.Fingerprint {
			result.Keys = []sshflow.VaultAgentKey{sshVaultAgentKey(inventory.Agent, key)}
			break
		}
	}
	result.AgentStatus = "not_visible"
	if len(result.Keys) > 0 {
		result.AgentStatus = "offered"
		result.Notes = append(result.Notes, "The exact provider agent advertises this fingerprint. Listing does not prove signing or SSH authentication; normal SSH review and proof are still required.")
	} else {
		result.Notes = append(result.Notes, "The item was created but is not yet visible in the captured provider agent. Sync/unlock the native app and refresh the picker; retain the item ID and do not repeat creation automatically.")
	}
	return result, nil
}

func applySSHBitwardenHandoff(ctx context.Context, prepared *sshVaultKeyPlan, result sshflow.VaultKeyResult) (sshflow.VaultKeyResult, error) {
	if prepared.baseline == nil || !prepared.baseline.Complete {
		return result, errors.New("Bitwarden handoff has no complete captured baseline")
	}
	inventory, err := prepared.hosts.RefreshAgentKeys(ctx, *prepared.baseline)
	if err != nil || !inventory.Complete {
		result.Status, result.AgentStatus = "unavailable", "unavailable"
		result.Notes = []string{"Native desktop changes, if any, remain in Bitwarden. The captured agent could not be refreshed; no vault item ID or creation outcome was observed."}
		return result, errors.New("Bitwarden agent changed or its fresh inventory is incomplete; reopen the handoff without assuming an empty baseline")
	}
	before := make(map[string]bool, len(prepared.baseline.Candidates))
	for _, key := range prepared.baseline.Candidates {
		before[key.Fingerprint] = true
	}
	for _, key := range inventory.Candidates {
		if !before[key.Fingerprint] {
			result.Keys = append(result.Keys, sshVaultAgentKey(inventory.Agent, key))
		}
	}
	result.Status, result.AgentStatus = "observed", "offered"
	result.Notes = []string{"These fingerprints became visible in the same captured Bitwarden agent. That is not proof of newly created vault items; no item ID was observed.", "Choose the exact fingerprint explicitly, even when only one is listed. Listing is not signing or authentication proof."}
	if len(result.Keys) == 0 {
		result.Status, result.AgentStatus = "no_new_visible_keys", "not_visible"
		result.Notes = append(result.Notes, "No newly visible fingerprint was returned; native desktop changes, if any, were not undone.")
	}
	return result, nil
}

func sshVaultAgentKey(ref sshhost.AgentSocketRef, key sshhost.KeyCandidate) sshflow.VaultAgentKey {
	return sshflow.VaultAgentKey{Provider: string(ref.Provider), Socket: ref.Socket, Fingerprint: key.Fingerprint, Algorithm: key.Algorithm, Comment: key.Comment}
}

func sshVaultReceiptsForItems(items []sshOnboardItem) []sshflow.VaultKeyReceipt {
	var receipts []sshflow.VaultKeyReceipt
	for _, item := range items {
		for _, receipt := range item.Options.vaultReceipts {
			receipts = sshflow.RetainVaultKeyReceipt(receipts, &receipt)
		}
	}
	return receipts
}

func publicSSHVaultError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{sshvault.ErrUnknown, sshvault.ErrPublicKey, context.Canceled, context.DeadlineExceeded, sshvault.ErrExperimental, sshvault.ErrUnsupportedContext, sshvault.ErrNativeContext, sshvault.ErrEntrypoint, sshvault.ErrLocked, sshvault.ErrUnsupportedVersion, sshvault.ErrStale, sshvault.ErrPlanUsed, sshvault.ErrUnsafe, sshvault.ErrUnavailable} {
		if errors.Is(err, known) {
			return known
		}
	}
	return errors.New("SSH vault operation did not complete; inspect the retained public receipt before another attempt")
}
