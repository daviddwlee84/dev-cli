package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

func sshFieldChoices(key string) []string {
	switch key {
	case "auth":
		return []string{"config", "existing", "key"}
	case "os":
		return []string{"posix", "windows"}
	case "fleet", "herdr":
		return []string{"no", "yes"}
	}
	return nil
}

func sshRenderChoices(choices []string, current string) string {
	parts := make([]string, len(choices))
	for i, choice := range choices {
		parts[i] = choice
		if choice == current {
			parts[i] = "[" + choice + "]"
		}
	}
	return strings.Join(parts, " · ")
}

func sshKeyDisplay(path string, generate bool) string {
	if generate && path != "" {
		return "new: " + path
	}
	return path
}

func sshProfileEndpoint(profile sshflow.ConnectionProfile) string {
	host := profile.HostName
	if host == "" {
		host = "native config"
	}
	if profile.User != "" {
		host = profile.User + "@" + host
	}
	if profile.Port > 0 {
		host += ":" + strconv.Itoa(profile.Port)
	}
	return host
}

func (d *sshDialog) setField(key, value string) {
	for i := 0; i < d.fieldCount; i++ {
		if d.fields[i].key == key {
			d.fields[i].input.SetValue(value)
		}
	}
}

func (d *sshDialog) applyKeyChoice(path string, generate bool) {
	d.onboarding.KeyPath, d.onboarding.GenerateKey = path, generate
	if !generate {
		d.onboarding.KeyComment = ""
	}
	d.setField("key", sshKeyDisplay(path, generate))
	d.setField("auth", "key")
	d.err = nil
}

// openSSHKeyForm installs a key for an existing profile without editable
// connection fields, starting with the key picker.
func (m Model) openSSHKeyForm(profile sshflow.ConnectionProfile) (tea.Model, tea.Cmd) {
	m.pauseSSHBackground()
	p := profile
	request := sshflow.OnboardRequest{Profile: &p, Alias: p.Alias, HostName: p.HostName, User: p.User, Port: p.Port, Auth: "key", RemoteOS: "posix"}
	d := sshDialog{kind: "onboard", title: fmt.Sprintf("Install SSH key for %s (%s)", p.Alias, sshProfileEndpoint(p)), onboarding: request}
	d.addField("key", "Key", "")
	d.addField("os", "Remote OS", request.RemoteOS)
	d.addField("fleet", "Fleet", "no")
	d.addField("herdr", "Herdr", "no")
	m.sshUI.dialog = d
	if m.actions.SSH.ListKeys == nil {
		return m, m.focusSSHField(0)
	}
	return m.openSSHKeyPicker()
}

func (m Model) openSSHKeyPicker() (tea.Model, tea.Cmd) {
	parent := m.sshUI.dialog
	alias := parent.value("alias")
	if parent.onboarding.Profile != nil {
		alias = parent.onboarding.Profile.Alias
	}
	if alias == "" {
		alias = "host"
	}
	m.sshUI.generation++
	generation := m.sshUI.generation
	m.sshUI.dialog = sshDialog{kind: "keys-loading", title: "Choose SSH key for " + alias, body: "Reading local SSH keys…", parent: &parent}
	return m, func() tea.Msg {
		keys, err := m.actions.SSH.ListKeys(m.baseContext(), alias)
		return sshEventMsg{generation: generation, kind: "keys", keys: keys, err: err}
	}
}

func (m Model) chooseSSHKey(index int) (tea.Model, tea.Cmd) {
	d := m.sshUI.dialog
	if d.parent == nil {
		return m, nil
	}
	parent := *d.parent
	if index < len(d.keys) {
		choice := d.keys[index]
		if choice.Generate {
			path := choice.Path
			if parent.onboarding.GenerateKey && parent.onboarding.KeyPath != "" {
				path = parent.onboarding.KeyPath
			}
			form := sshDialog{kind: "keygen", title: "Name the new SSH key", parent: &parent}
			form.addField("path", "New key path", path)
			form.addField("comment", "Comment", parent.onboarding.KeyComment)
			m.sshUI.dialog = form
			return m, m.focusSSHField(0)
		}
		parent.applyKeyChoice(choice.Path, false)
		m.sshUI.dialog = parent
		return m, m.focusSSHField(parent.index)
	}
	form := sshDialog{kind: "keypath", title: d.title, parent: &parent}
	path := ""
	if !parent.onboarding.GenerateKey {
		path = parent.onboarding.KeyPath
	}
	form.addField("path", "Key path", path)
	m.sshUI.dialog = form
	return m, m.focusSSHField(0)
}

func (m Model) finishSSHKeyGenerate() (tea.Model, tea.Cmd) {
	d := m.sshUI.dialog
	if d.parent == nil {
		return m, nil
	}
	path := strings.TrimSpace(d.value("path"))
	if path == "" {
		m.sshUI.dialog.err = errors.New("Enter a new key path, or Esc to return to the form")
		return m, nil
	}
	parent := *d.parent
	parent.applyKeyChoice(path, true)
	parent.onboarding.KeyComment = strings.TrimSpace(d.value("comment"))
	m.sshUI.dialog = parent
	return m, m.focusSSHField(parent.index)
}

func (m Model) finishSSHKeyPath() (tea.Model, tea.Cmd) {
	d := m.sshUI.dialog
	if d.parent == nil {
		return m, nil
	}
	path := strings.TrimSpace(d.value("path"))
	if path == "" {
		m.sshUI.dialog.err = errors.New("Enter a key path, or Esc to return to the form")
		return m, nil
	}
	parent := *d.parent
	parent.applyKeyChoice(path, false)
	m.sshUI.dialog = parent
	return m, m.focusSSHField(parent.index)
}
