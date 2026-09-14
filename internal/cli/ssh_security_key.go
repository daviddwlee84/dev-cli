package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

var errSSHFleetImportHardware = errors.New("new hardware-key generation is not supported while importing connection or hop profiles FROM fleet; import configuration first, then run regular dev ssh setup <local-alias> where ownership allows. Registering a regular alias TO fleet with --to fleet|both remains supported")

func sshHardwareKeyType(value string) bool {
	return value == string(sshhost.KeyTypeEd25519SK) || value == string(sshhost.KeyTypeECDSASK)
}

func validateSSHGenerationOptions(options sshSetupOptions) error {
	keyType := options.keyType
	if keyType == "" {
		keyType = string(sshhost.KeyTypeEd25519)
	}
	if keyType != string(sshhost.KeyTypeEd25519) && !sshHardwareKeyType(keyType) {
		return errors.New("--key-type must be ed25519, ed25519-sk or ecdsa-sk")
	}
	hasSecurityOptions := options.securityKeyFlagsChanged || options.securityKey != (sshhost.SecurityKeyOptions{})
	if !options.generateKey && (options.keyGenerationFlagsChanged || keyType != string(sshhost.KeyTypeEd25519) || hasSecurityOptions) {
		return errors.New("--key-type and --sk-* options require explicit --generate-key")
	}
	if hasSecurityOptions && !sshHardwareKeyType(keyType) {
		return errors.New("--sk-* options require --key-type ed25519-sk or ecdsa-sk; ordinary Ed25519 generation is never a hardware fallback")
	}
	if !options.generateKey || !sshHardwareKeyType(keyType) {
		return nil
	}
	if options.configOnly || options.auth != "" || options.hasExistingKey() || options.identityAgent != "" || options.agentRef != nil {
		return errors.New("security-key generation cannot be combined with existing-key, agent, --auth or --config-only selection")
	}
	if options.identityFileChanged || options.identitiesOnlyChanged && !options.identitiesOnly {
		return errors.New("hardware-key generation supplies its reviewed IdentityFile and IdentitiesOnly=yes; do not override either with incompatible connection flags")
	}
	if options.fleetImportKeyPicker || strings.HasPrefix(options.from, "fleet:") || strings.HasPrefix(options.from, "fleet-ssh:") {
		return errSSHFleetImportHardware
	}
	return nil
}

func clearSSHGeneratedKeyOptions(options *sshSetupOptions) {
	options.keyType, options.comment = "", ""
	options.securityKey = sshhost.SecurityKeyOptions{}
	options.keyGenerationFlagsChanged, options.securityKeyFlagsChanged = false, false
	options.noPassphrase = false
}

func promptSSHSecurityKeyOptions(prompt *prompter, options *sshSetupOptions) error {
	var err error
	options.keyType, err = prompt.choiceOf("Security key type", string(sshhost.KeyTypeEd25519SK), []string{"ed25519-sk", "ecdsa-sk"}, map[string]string{"ed25519-sk": "ed25519-sk", "ecdsa-sk": "ecdsa-sk"})
	if err != nil {
		return err
	}
	options.securityKey.Provider, err = prompt.line("SecurityKeyProvider (internal or absolute library path)", "internal")
	if err != nil {
		return err
	}
	resident, err := prompt.choiceOf("Resident credential (may remain on the device after cancellation)", "no", []string{"no", "yes"}, map[string]string{"no": "no", "n": "no", "yes": "yes", "y": "yes"})
	if err != nil {
		return err
	}
	verify, err := prompt.choiceOf("Require user verification for signing", "no", []string{"no", "yes"}, map[string]string{"no": "no", "n": "no", "yes": "yes", "y": "yes"})
	if err != nil {
		return err
	}
	options.securityKey.Resident, options.securityKey.VerifyRequired = resident == "yes", verify == "yes"
	options.securityKey.Application, err = prompt.line("Application (ssh: label; blank uses the reviewed default)", "")
	return err
}

// sshSecurityKeyChoices uses only service-owned stat observations. It never
// probes hardware, runs a provider or claims signing capability was proved.
func sshSecurityKeyChoices(ctx context.Context, service *sshhost.Service, alias string, importPicker bool) ([]tui.SSHKeyChoice, error) {
	observation, err := service.ObserveSecurityKeyCapability(ctx, "internal")
	if err != nil {
		return nil, err
	}
	description := "Hardware signing key; native PIN/touch required. Capability is unverified until an explicit attempt."
	reason := ""
	if !observation.CanAttempt {
		reason = "Security-key generation is unavailable with the current OpenSSH tools."
		for _, diagnostic := range observation.Diagnostics {
			if diagnostic.Message != "" {
				reason += " " + diagnostic.Message
			}
		}
	}
	if importPicker {
		reason = errSSHFleetImportHardware.Error()
	}
	choice := tui.SSHKeyChoice{Label: "+ Generate on a security key (YubiKey / FIDO2)", Description: description, Path: filepath.Join(service.Paths().SSHDir, "id_ed25519_sk_dev_"+alias), Generate: true, KeyType: sshhost.KeyTypeEd25519SK, UnavailableReason: reason}
	if reason != "" {
		choice.Description = reason
	}
	choices := []tui.SSHKeyChoice{choice}
	if runtime.GOOS == "darwin" {
		reason := "Automatic Apple Secure Enclave creation is unavailable: create a key with Secretive and select its agent, or select an existing security-key stub. Dev will not run sc_auth or download resident keys during discovery."
		choices = append(choices, tui.SSHKeyChoice{Label: "Apple Secure Enclave generation (unavailable)", Description: reason, UnavailableReason: reason})
	}
	return choices, nil
}

func prepareSSHSecurityKeyConfig(ctx context.Context, service *sshhost.Service, alias, class string, options sshSetupOptions, plan sshhost.KeyPlan, definition *sshhost.ManagedDefinition) error {
	if !sshHardwareKeyType(string(plan.KeyType)) || plan.Operation != sshhost.KeyGenerate {
		return nil
	}
	if err := validateSSHGenerationOptions(options); err != nil {
		return err
	}
	if !plan.Ready() {
		return sshKeyPlanBlockedError(plan)
	}
	if class == "foreign" {
		if options.dryRun {
			return fmt.Errorf("foreign alias %s requires a fresh SecurityKeyProvider policy check before hardware generation; dry-run never evaluates its native configuration: %w", alias, sshhost.ErrManualRemediation)
		}
		if err := service.VerifySecurityKeyPolicy(ctx, alias, plan); err != nil {
			return fmt.Errorf("foreign alias %s is not rewritten; configure its matching SecurityKeyProvider and reviewed identity before retrying: %w", alias, err)
		}
		return nil
	}
	definition.IdentityFile, definition.SecurityKeyProvider = plan.IdentityFile, plan.SecurityKeyProvider
	only := true
	definition.IdentitiesOnly = &only
	return nil
}

func sshHardwarePlanNotes(plan sshhost.KeyPlan) []string {
	if plan.Operation != sshhost.KeyGenerate || !sshHardwareKeyType(string(plan.KeyType)) {
		return nil
	}
	notes := []string{
		"Hardware key type: " + string(plan.KeyType),
		"Local key handle: " + plan.IdentityFile + " (not an exportable private signing key).",
		"Public key: " + plan.PublicPath,
		"ssh-keygen: " + plan.KeygenPath,
		"SSH client: " + plan.SSHClientPath,
		"SecurityKeyProvider: " + plan.SecurityKeyProvider + " (v2 for managed aliases).",
		"Managed v2 fragments require dev newer than v0.2.37.",
		fmt.Sprintf("Resident credential: %t; verification required: %t; application: %s", plan.SecurityKey.Resident, plan.SecurityKey.VerifyRequired, plan.SecurityKey.Application),
		"Native PIN/touch interaction is required; no downgrade or unattended fallback is attempted.",
		"Hardware credentials may remain after cancellation or local failure; dev never claims hardware rollback or retries creation automatically.",
	}
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Message != "" {
			notes = append(notes, diagnostic.Message)
		}
	}
	return notes
}

func sshHardwareResultNotes(result sshhost.KeyResult) []string {
	if result.Hardware == nil {
		return sshLocalKeyFileNotes(result.LocalFiles)
	}
	effect := result.Hardware
	notes := []string{fmt.Sprintf("Hardware key %s: %s (resident=%t); no hardware rollback is implied.", effect.Kind, effect.Status, effect.Resident)}
	identity, public := effect.RecoveryIdentityPath, effect.RecoveryPublicPath
	if identity == "" {
		identity = result.Candidate.IdentityFile
	}
	if public == "" {
		public = result.Candidate.PublicPath
	}
	if identity != "" {
		notes = append(notes, "Retained key handle: "+identity)
	}
	if public != "" {
		notes = append(notes, "Retained public key: "+public)
	}
	if effect.Status == "unknown" {
		notes = append(notes, "Inspect the device and retained paths before another creation attempt; do not retry automatically.")
	}
	return append(notes, sshLocalKeyFileNotes(result.LocalFiles)...)
}

func sshLocalKeyFileNotes(files []sshhost.KeyLocalFileObservation) []string {
	var notes []string
	for _, file := range files {
		notes = append(notes, fmt.Sprintf("Local file observation: %s %s %s (not a validated recovery pair).", file.Kind, file.Status, file.Path))
	}
	if len(files) > 0 {
		notes = append(notes, "Inspect these local paths before retrying; observations do not authorize key use or recovery.")
	}
	return notes
}
