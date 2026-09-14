package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

func (m Model) updateSSHDialog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.sshUI.dialog
	key := msg.String()
	if (key == "esc" || key == "ctrl+c") && d.parent != nil && (d.kind == "keys" || d.kind == "keys-loading" || d.kind == "keypath") {
		parent := *d.parent
		m.sshUI.generation++
		m.sshUI.dialog = parent
		return m, m.focusSSHField(parent.index)
	}
	if key == "esc" || key == "ctrl+c" {
		if m.sshUI.running && !m.sshUI.background {
			if m.sshUI.cancel != nil {
				m.sshUI.cancel()
			}
			d.body += "\nCanceling; retaining completed observations…"
			return m, nil
		}
		m.sshUI.generation++
		m.sshUI.dialog = sshDialog{}
		cmd := m.scheduleSSHBackground()
		return m, cmd
	}
	if d.kind == "discovering" || d.kind == "testing" || d.kind == "loading" || d.kind == "preparing" || d.kind == "keys-loading" {
		return m, nil
	}
	if d.fieldCount > 0 && (d.kind == "lan" || d.kind == "onboard" || d.kind == "keypath") {
		field := &d.fields[d.index]
		switch key {
		case "tab", "down":
			return m, m.focusSSHField(d.index + 1)
		case "shift+tab", "up":
			return m, m.focusSSHField(d.index - 1)
		case " ", "left", "right":
			if d.kind == "onboard" && field.key == "key" {
				if key == " " && m.actions.SSH.ListKeys != nil {
					return m.openSSHKeyPicker()
				}
				return m, nil
			}
			if choices := sshFieldChoices(field.key); d.kind == "onboard" && choices != nil {
				step := 1
				if key == "left" {
					step = len(choices) - 1
				}
				i := max(0, slices.Index(choices, field.input.Value()))
				field.input.SetValue(choices[(i+step)%len(choices)])
				return m, nil
			}
		case "enter":
			if d.kind == "onboard" && field.key == "key" && m.actions.SSH.ListKeys != nil {
				return m.openSSHKeyPicker()
			}
			if d.kind == "keypath" {
				return m.finishSSHKeyPath()
			}
			if d.index < d.fieldCount-1 {
				return m, m.focusSSHField(d.index + 1)
			}
			fallthrough
		case "ctrl+s":
			if d.kind == "keypath" {
				return m.finishSSHKeyPath()
			}
			if d.kind == "onboard" {
				return m.prepareSSHOnboarding()
			}
			request := d.request
			request.LAN.Interface = d.value("interface")
			request.LAN.Ranges = strings.FieldsFunc(d.value("ranges"), func(r rune) bool { return r == ',' || r == ' ' })
			for _, value := range strings.FieldsFunc(d.value("ports"), func(r rune) bool { return r == ',' || r == ' ' }) {
				port, err := strconv.Atoi(value)
				if err != nil || port < 1 || port > 65535 {
					d.err = fmt.Errorf("invalid port %q", value)
					return m, nil
				}
				request.LAN.Ports = append(request.LAN.Ports, port)
			}
			if len(request.LAN.Ranges) == 0 {
				d.err = fmt.Errorf("select an explicit IPv4 range")
				return m, nil
			}
			return m.startSSHDiscovery(request, false)
		}
		if d.kind == "onboard" && (field.key == "key" || sshFieldChoices(field.key) != nil) {
			return m, nil
		}
		var cmd tea.Cmd
		d.fields[d.index].input, cmd = d.fields[d.index].input.Update(msg)
		return m, cmd
	}
	if len(d.options) == 0 && (d.kind == "message" || d.kind == "review" || d.kind == "test-review") {
		switch key {
		case "j", "down":
			d.scroll++
			return m, nil
		case "k", "up":
			d.scroll = max(0, d.scroll-1)
			return m, nil
		case "pgdown":
			d.scroll += max(1, m.height-10)
			return m, nil
		case "pgup":
			d.scroll = max(0, d.scroll-max(1, m.height-10))
			return m, nil
		case "home":
			d.scroll = 0
			return m, nil
		}
	}
	switch key {
	case "j", "down":
		if d.index < len(d.options)-1 {
			d.index++
		}
		return m, nil
	case "k", "up":
		if d.index > 0 {
			d.index--
		}
		return m, nil
	case "enter":
		switch d.kind {
		case "source":
			if d.index == 0 {
				return m.startSSHDiscovery(SSHDiscoveryRequest{Source: "tailscale"}, false)
			}
			return m.loadSSHInterfaces()
		case "interfaces":
			if d.index < len(d.interfaces) {
				return m.openSSHLANForm(d.interfaces[d.index])
			}
		case "review":
			return m.applySSHOnboarding()
		case "keys":
			return m.chooseSSHKey(d.index)
		case "message":
			m.sshUI.dialog = sshDialog{}
			cmd := m.scheduleSSHBackground()
			return m, cmd
		case "profiles":
			if d.index >= len(d.profiles) {
				return m, nil
			}
			profile := d.profiles[d.index]
			if d.workflow == "install-key" {
				return m.openSSHKeyForm(profile)
			}
			row, _ := m.currentSSH()
			workflow, err := m.actions.SSH.Workflow(m.baseContext(), SSHWorkflowRequest{Action: d.workflow, Selected: row, Profile: &profile})
			if err != nil {
				d.err = err
				return m, nil
			}
			m.sshUI.dialog = sshDialog{}
			return m, tea.Exec(workflow, func(err error) tea.Msg { return afterExec(sshWorkflowMsg{result: workflow.Result(), err: err}) })
		case "endpoints":
			var row SSHRow
			if err := json.Unmarshal([]byte(d.body), &row); err != nil {
				d.err = err
				return m, nil
			}
			index := 0
			for _, candidate := range append(slices.Clone(row.LAN), row.Tailscale...) {
				for _, address := range candidate.Addresses {
					if index == d.index {
						r := d.onboarding
						c := candidate
						r.Candidate = &c
						r.RemoteOS = "posix"
						if c.OS == "windows" {
							r.RemoteOS = "windows"
						}
						r.HostName = address
						r.Port = c.Port
						r.Report = m.sshCandidateReport(c)
						return m.openSSHOnboardingForm(r)
					}
					index++
				}
			}
		case "tests":
			return m.reviewSSHTests(d.index)
		case "test-review":
			return m.startSSHTests(d.tests)
		}
	}
	return m, nil
}

func (m Model) renderSSHDialog() string {
	d := m.sshUI.dialog
	width := max(1, m.width-4)
	lines := []string{"  " + d.title, ""}
	if d.kind == "lan" {
		lines = append(lines, "  Explicit local scan: up to 256 IPv4 addresses, 16 ports, 30 seconds.", "")
	}
	if d.kind == "onboard" {
		summary := "  Save SSH config by default. Fleet / Herdr require authentication."
		if d.onboarding.Profile != nil {
			summary = "  Installs the selected public key; connection settings stay as configured."
		}
		lines = append(lines, summary, "  ←/→ or Space changes choices · Enter on Key lists keys · Ctrl+S reviews.", "")
	}
	if d.kind == "keypath" {
		lines = append(lines, "  Existing private or public key path. Enter confirms · Esc returns to the form.", "")
	}
	if d.kind == "discovering" || d.kind == "testing" {
		lines = append(lines, fmt.Sprintf("  Progress %d/%d · found %d · Esc cancels", d.completed, d.total, d.found), "")
	}
	if d.kind == "review" {
		lines = append(lines, "  Review exact effects below. Enter applies; Esc cancels.", "")
	}
	if d.fieldCount > 0 && (d.kind == "lan" || d.kind == "onboard" || d.kind == "keypath") {
		limit := max(1, m.height-11)
		from := max(0, d.index-limit+1)
		to := min(d.fieldCount, from+limit)
		for i := from; i < to; i++ {
			field := d.fields[i]
			marker := "  "
			if i == d.index {
				marker = "› "
			}
			value := field.input.View()
			switch field.key {
			case "fleet", "herdr":
				value = "[ ]"
				if field.input.Value() == "yes" {
					value = "[x]"
				}
			case "auth", "os":
				value = sshRenderChoices(sshFieldChoices(field.key), field.input.Value())
			case "key":
				if d.kind == "onboard" {
					value = field.input.Value()
					if value == "" {
						value = "‹ Enter: choose an existing key or generate one ›"
					}
				}
			}
			lines = append(lines, "  "+marker+sshPad(field.label, 15)+" "+value)
		}
		lines = append(lines, "", "  Tab moves · Space choices · Ctrl+S review/start · Esc cancel")
	} else if len(d.options) > 0 {
		limit := max(4, m.height-9)
		from := max(0, d.index-limit+1)
		to := min(len(d.options), from+limit)
		for i := from; i < to; i++ {
			marker := "  "
			if i == d.index {
				marker = "› "
			}
			lines = append(lines, "  "+marker+d.options[i])
		}
		footer := "  ↑/↓ choose · Enter select · Esc cancel"
		if d.kind == "keys" {
			footer = "  ↑/↓ choose · Enter select · Esc returns to the form"
		}
		lines = append(lines, "", footer)
	} else {
		body := strings.Split(ansi.Hardwrap(d.body, max(1, width-2), true), "\n")
		limit := max(3, m.height-10)
		from := min(d.scroll, max(0, len(body)-limit))
		to := min(len(body), from+limit)
		if d.kind == "discovering" || d.kind == "testing" {
			from = max(0, len(body)-limit)
			to = len(body)
		}
		more := len(body) > limit
		body = body[from:to]
		if more {
			lines = append(lines, fmt.Sprintf("  Lines %d–%d · ↑/↓ / PgUp/PgDn scroll", from+1, to))
		}
		for _, line := range body {
			lines = append(lines, "  "+line)
		}
		if d.kind == "message" {
			lines = append(lines, "", "  Enter / Esc returns to connections")
		}
	}
	if d.err != nil {
		lines = append(lines, "", "  ! "+d.err.Error(), "  Esc returns · Ctrl+O opens issues and suggested actions")
	}
	for i := range lines {
		lines[i] = fitCell(lines[i], width)
	}
	return strings.Join(lines, "\n") + "\n"
}

func (m Model) openSSHTests() (tea.Model, tea.Cmd) {
	m.pauseSSHBackground()
	m.sshUI.dialog = sshDialog{kind: "tests", title: "Test SSH connections", options: []string{"Quick connectivity — selected profile", "Full SSH verification — selected profile", "Quick connectivity — this machine", "Full SSH verification — this machine", "Quick connectivity — filtered profiles", "Full SSH verification — filtered profiles"}}
	return m, nil
}
func (m Model) reviewSSHTests(choice int) (tea.Model, tea.Cmd) {
	request := SSHTestRequest{Full: choice%2 == 1}
	switch choice / 2 {
	case 0:
		entry, ok := m.currentSSHEntry()
		if ok {
			if entry.profile != nil {
				request.Profiles = []sshflow.ConnectionProfile{*entry.profile}
			} else if len(entry.machine.Profiles) == 1 {
				request.Profiles = slices.Clone(entry.machine.Profiles)
			} else if len(entry.machine.Profiles) > 1 {
				// An exact child must be selected before a one-profile test.
				m.sshUI.dialog.err = fmt.Errorf("expand the machine with Space and select a profile, or choose this machine")
				return m, nil
			}
		}
	case 1:
		row, ok := m.currentSSH()
		if ok {
			request.Profiles = slices.Clone(row.Profiles)
		}
	case 2:
		seen := map[string]bool{}
		for _, entry := range m.visibleSSHEntries() {
			for _, profile := range entry.machine.Profiles {
				if !seen[profile.ID] && matches(profile.Alias+" "+profile.HostName+" "+entry.machine.Label, m.filter) {
					request.Profiles = append(request.Profiles, profile)
					seen[profile.ID] = true
				}
			}
		}
	}
	if len(request.Profiles) == 0 {
		m.sshUI.dialog.err = fmt.Errorf("no configured profiles in this selection")
		return m, nil
	}
	body := fmt.Sprintf("%d exact profiles; at most 4 concurrent, 30 seconds per profile.\n", len(request.Profiles))
	if request.Full {
		body += "Full verification includes SSH authentication.\n"
	} else {
		body += "Quick checks do not authenticate.\n"
	}
	for _, p := range request.Profiles {
		body += p.Alias + " → " + p.HostName + "\n"
	}
	body += "\nEnter starts these checks; Esc cancels."
	m.sshUI.dialog = sshDialog{kind: "test-review", title: "Review SSH tests", body: body, tests: request}
	return m, nil
}
func (m Model) startSSHTests(request SSHTestRequest) (tea.Model, tea.Cmd) {
	if m.sshUI.running {
		if m.sshUI.cancel != nil {
			m.sshUI.cancel()
		}
	}
	m.sshUI.generation++
	m.sshUI.backgroundGeneration++
	m.sshUI.backgroundScheduled = false
	generation := m.sshUI.generation
	ctx, cancel := context.WithCancel(m.baseContext())
	events := make(chan sshEventMsg, 32)
	m.sshUI.events = events
	m.sshUI.cancel = cancel
	m.sshUI.running = true
	m.sshUI.background = false
	m.sshUI.dialog = sshDialog{kind: "testing", title: "Testing SSH connections", total: len(request.Profiles)}
	return m, func() tea.Msg {
		go func() {
			defer close(events)
			defer cancel()
			result, err := m.actions.SSH.Test(ctx, request, func(progress SSHTestProgress) {
				select {
				case events <- sshEventMsg{generation: generation, kind: "test-progress", test: progress}:
				case <-ctx.Done():
				}
			})
			finishSSHEvent(events, sshEventMsg{generation: generation, kind: "tested", testResult: result, err: err})
		}()
		return <-events
	}
}

func (m Model) updateSSHDialogMouse(message tea.MouseMsg) (tea.Model, tea.Cmd) {
	event := tea.MouseEvent(message)
	if event.Button == tea.MouseButtonWheelDown {
		return m.updateSSHDialog(tea.KeyMsg{Type: tea.KeyDown})
	}
	if event.Button == tea.MouseButtonWheelUp {
		return m.updateSSHDialog(tea.KeyMsg{Type: tea.KeyUp})
	}
	// Native forms and reviews never pass screen coordinates through to the
	// dashboard hidden beneath them.
	return m, nil
}
