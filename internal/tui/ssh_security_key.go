package tui

import (
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func sshHardwareKeyType(value string) bool {
	return value == string(sshhost.KeyTypeEd25519SK) || value == string(sshhost.KeyTypeECDSASK)
}

func sshKeyGenerationForm(parent sshDialog, choice SSHKeyChoice) sshDialog {
	path := choice.Path
	if parent.onboarding.GenerateKey && parent.onboarding.KeyPath != "" {
		path = parent.onboarding.KeyPath
	}
	keyType := choice.KeyType
	if keyType == "" {
		keyType = sshhost.KeyTypeEd25519
	}
	if parent.onboarding.GenerateKey && sshHardwareKeyType(string(keyType)) && sshHardwareKeyType(string(parent.onboarding.KeyType)) {
		keyType = parent.onboarding.KeyType
	}
	security := parent.onboarding.SecurityKey
	if !sshHardwareKeyType(string(keyType)) {
		security = sshhost.SecurityKeyOptions{}
	} else if security.Provider == "" {
		security.Provider = "internal"
	}
	form := sshDialog{kind: "keygen", title: "Configure the new SSH key", parent: &parent}
	form.addField("path", "New key path", path)
	form.addField("comment", "Comment", parent.onboarding.KeyComment)
	form.addField("keytype", "Key type", string(keyType))
	form.addField("skprovider", "SK provider", security.Provider)
	form.addField("skresident", "Resident", "no")
	if security.Resident {
		form.setField("skresident", "yes")
	}
	form.addField("skverify", "Verify required", "no")
	if security.VerifyRequired {
		form.setField("skverify", "yes")
	}
	form.addField("skapplication", "SK application", security.Application)
	return form
}

func (d *sshDialog) updateSSHKeyTypeFields() {
	if !sshHardwareKeyType(d.value("keytype")) {
		d.setField("skprovider", "")
		d.setField("skresident", "no")
		d.setField("skverify", "no")
		d.setField("skapplication", "")
	} else if d.value("skprovider") == "" {
		d.setField("skprovider", "internal")
	}
}

func sshHardwareResultLines(result sshhost.KeyResult) []string {
	if result.Hardware == nil {
		return sshLocalKeyFileLines(result.LocalFiles)
	}
	effect := result.Hardware
	lines := []string{fmt.Sprintf("Hardware key %s: %s (resident=%t); no hardware rollback is implied.", effect.Kind, effect.Status, effect.Resident)}
	identity, public := effect.RecoveryIdentityPath, effect.RecoveryPublicPath
	if identity == "" {
		identity = result.Candidate.IdentityFile
	}
	if public == "" {
		public = result.Candidate.PublicPath
	}
	if identity != "" {
		lines = append(lines, "Retained key handle: "+identity)
	}
	if public != "" {
		lines = append(lines, "Retained public key: "+public)
	}
	if effect.Status == "unknown" {
		lines = append(lines, "Inspect the device and retained paths before another creation attempt; do not retry automatically.")
	}
	return append(lines, sshLocalKeyFileLines(result.LocalFiles)...)
}

func sshLocalKeyFileLines(files []sshhost.KeyLocalFileObservation) []string {
	var lines []string
	for _, file := range files {
		lines = append(lines, fmt.Sprintf("Local file observation: %s %s %s (not a validated recovery pair).", file.Kind, file.Status, file.Path))
	}
	if len(files) > 0 {
		lines = append(lines, "Inspect these local paths before retrying; observations do not authorize key use or recovery.")
	}
	return lines
}
