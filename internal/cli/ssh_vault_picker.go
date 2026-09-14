package cli

import (
	"context"
	"errors"
	"os/exec"
	"runtime"

	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func sshVaultRequestForAction(action, title string) (sshflow.VaultKeyRequest, error) {
	request := sshflow.VaultKeyRequest{Title: title}
	switch action {
	case "1password":
		request.Provider = "1password"
	case "bitwarden-native":
		request.Provider = "bitwarden"
	case "bitwarden-desktop":
		request.Provider, request.Desktop = "bitwarden", true
	default:
		return request, errors.New("choose an explicitly supported vault creation action")
	}
	return request, nil
}

// sshVaultKeyChoices uses PATH lookup and socket stat observations only. Native
// provider clients, profile parsing and agent inventory belong to explicit actions.
func sshVaultKeyChoices(service *sshhost.Service, importPicker bool) []tui.SSHKeyChoice {
	choices := []tui.SSHKeyChoice{
		{Label: "+ Create in a 1Password vault", Description: "Separate vault review; only its public receipt is retained by dev", VaultAction: "1password"},
		{Label: "+ Create/unlock in Bitwarden desktop", Description: "Manual GUI handoff; newly visible agent keys are not item-creation proof", VaultAction: "bitwarden-desktop"},
		{Label: "+ Generate into Bitwarden (experimental native context)", Description: "Separate experimental + native-profile approvals; endpoints stay unverified", VaultAction: "bitwarden-native"},
	}
	for index := range choices {
		choice := &choices[index]
		if importPicker {
			choice.UnavailableReason = "New vault creation is not supported while importing profiles FROM fleet; import configuration first, then use regular SSH key setup. Registration TO fleet remains separate and supported."
		} else if choice.VaultAction == "bitwarden-desktop" {
			present := false
			for _, observation := range service.ObserveAgentProviders(runtime.GOOS) {
				present = present || observation.Provider == sshhost.AgentProviderBitwarden && observation.SocketPresent
			}
			if !present {
				choice.UnavailableReason = "The Bitwarden SSH agent socket was not found. Enable/unlock the native desktop agent, then reopen this handoff."
			}
		} else {
			tool := "op"
			if choice.VaultAction == "bitwarden-native" {
				tool = "bw"
			}
			if _, err := exec.LookPath(tool); err != nil {
				choice.UnavailableReason = tool + " was not found in PATH. Install/authenticate the native provider CLI yourself, then reopen the picker; dev will not install or unlock it."
			}
		}
		if choice.UnavailableReason != "" {
			choice.Label += " (unavailable)"
			choice.Description = choice.UnavailableReason
		}
	}
	return choices
}

func pickSSHVaultAgentKey(ctx context.Context, app *App, result sshflow.VaultKeyResult) (*sshflow.VaultAgentKey, error) {
	if len(result.Keys) == 0 {
		return nil, nil
	}
	items := make([]picker.Item, 0, len(result.Keys)+1)
	for _, key := range result.Keys {
		items = append(items, picker.Item{Value: key.Fingerprint, Label: key.Fingerprint, Description: key.Provider + " · " + key.Algorithm + " · " + key.Comment})
	}
	items = append(items, picker.Item{Value: "none", Label: "Return without selecting a key", Description: "Any native vault item or desktop change is retained"})
	selected, err := sshPick(ctx, app, "Choose the exact advertised fingerprint (listing is not authentication proof)", items, false)
	if err != nil {
		return nil, err
	}
	if selected[0].Value == "none" {
		return nil, nil
	}
	for _, key := range result.Keys {
		if key.Fingerprint == selected[0].Value {
			return &key, nil
		}
	}
	return nil, errors.New("the vault key picker returned an unobserved fingerprint")
}
