package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

type SSHInventory = sshflow.MachineInventory
type SSHRow = sshflow.MachineRow

// SSHActions separates passive inventory from explicit foreground actions.
// Its loader reads local configuration and caches; it never discovers peers,
// scans a LAN, authenticates, or fetches remote repository observations.
type SSHActions struct {
	Load     func(context.Context) (SSHInventory, error)
	Workflow func(context.Context, SSHWorkflowRequest) (SSHWorkflow, error)
}

type SSHWorkflowRequest struct {
	Action   string
	Selected SSHRow
}
type SSHWorkflowResult struct {
	Status            string
	MembershipChanged bool
}
type SSHWorkflow interface {
	tea.ExecCommand
	Result() SSHWorkflowResult
}
type sshLoadedMsg struct {
	generation uint64
	inventory  SSHInventory
	err        error
}
type sshWorkflowMsg struct {
	result SSHWorkflowResult
	err    error
}

func (m Model) WithSSH(inventory SSHInventory) Model {
	m.ssh = inventory
	m.seedViewSnapshot(ViewSSH, perftrace.SourceCache, perftrace.FreshnessFresh, true)
	return m
}

func (m Model) reloadSSH() tea.Cmd {
	generation, ctx := m.viewLoad(ViewSSH).generation, m.viewContext(ViewSSH)
	return func() tea.Msg {
		finish := m.trace.Start(perftrace.TUIProducerSSH, perftrace.Fields{View: perftrace.ViewSSH, Generation: generation})
		if m.actions.SSH.Load == nil {
			finish(perftrace.OutcomeFailed)
			return sshLoadedMsg{generation: generation, err: errors.New("SSH inventory is unavailable in this dashboard session")}
		}
		inventory, err := m.actions.SSH.Load(ctx)
		outcome := perftrace.OutcomeSuccess
		if err != nil {
			outcome = perftrace.OutcomeFailed
		}
		finish(outcome)
		return sshLoadedMsg{generation: generation, inventory: inventory, err: err}
	}
}

func (m Model) applySSHLoad(message sshLoadedMsg) (tea.Model, tea.Cmd) {
	token := m.currentToken()
	if !m.applyViewResult(ViewSSH, message.generation, message.err == nil, perftrace.SourceLive, resultFreshness(message.err), len(message.inventory.Machines), message.err, message.err == nil) {
		return m, nil
	}
	if message.err == nil {
		m.ssh = message.inventory
		m.setViewStatus(ViewSSH, "Local connections and saved discovery observations; c discovers, p authenticates")
	}
	if m.view == ViewSSH {
		if !m.selectToken(token) {
			m.setAt(m.at())
		}
	}
	return m, nil
}

func (m Model) visibleSSH() []SSHRow {
	rows := make([]SSHRow, 0, len(m.ssh.Machines))
	for _, row := range m.ssh.Machines {
		if matches(sshRowSummary(row), m.filter) {
			rows = append(rows, row)
		}
	}
	return applyColumnSort(m, rows, func(row SSHRow, column string) sortCell { return textCell(sshCell(row, column)) })
}

func (m Model) currentSSH() (SSHRow, bool) {
	if m.view != ViewSSH {
		return SSHRow{}, false
	}
	rows := m.visibleSSH()
	if m.sshCursor < 0 || m.sshCursor >= len(rows) {
		return SSHRow{}, false
	}
	return rows[m.sshCursor], true
}

func sshCell(row SSHRow, column string) string {
	var values []string
	switch column {
	case "machine":
		return row.Label
	case "state":
		return row.State
	case "ssh":
		for _, p := range row.Profiles {
			values = append(values, p.Alias)
		}
	case "tailscale":
		for _, c := range row.Tailscale {
			v := c.Name
			if c.Online != nil && !*c.Online {
				v += " (offline)"
			}
			values = append(values, v)
		}
	case "lan":
		for _, c := range row.LAN {
			values = append(values, fmt.Sprintf("%s:%d", strings.Join(c.Addresses, ","), c.Port))
		}
	case "fleet":
		for _, p := range row.Fleet {
			values = append(values, p.Name)
		}
	case "herdr":
		for _, p := range row.Herdr {
			v := p.Label + "/" + p.Session
			if !p.Enabled {
				v += " (disabled)"
			}
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return "—"
	}
	return strings.Join(values, ", ")
}

func sshRowSummary(row SSHRow) string {
	data, _ := json.MarshalIndent(row, "", "  ")
	return string(data)
}

func (m Model) renderSSH() string {
	rows := m.visibleSSH()
	if len(rows) == 0 {
		if m.viewLoad(ViewSSH).loading {
			return "  Loading local connections and discovery cache…\n"
		}
		return "  No matching machines. Press n to set up connections or c to discover hosts.\n"
	}
	columns := []string{"machine", "ssh", "state"}
	if m.width >= 100 {
		columns = []string{"machine", "ssh", "tailscale", "lan", "fleet", "herdr", "state"}
	} else if m.width >= 75 {
		columns = []string{"machine", "ssh", "tailscale", "herdr", "state"}
	}
	cellWidth := max(7, (m.width-4-2*(len(columns)-1))/len(columns))
	line := func(values []string) string {
		for i := range values {
			values[i] = pad(values[i], cellWidth)
		}
		return strings.Join(values, "  ")
	}
	var header []string
	for _, column := range columns {
		header = append(header, strings.ToUpper(column))
	}
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(line(header)) + "\n")
	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		var values []string
		for _, column := range columns {
			value := sshCell(rows[i], column)
			if (column == "tailscale" || column == "lan") && value != "—" && m.ssh.Sources[column] == "stale" {
				value = "[stale] " + value
			}
			values = append(values, value)
		}
		plain := line(values)
		styled := plain
		if rows[i].State == "stale" || rows[i].State == "unresolved" {
			styled = styleDrift.Render(plain)
		}
		b.WriteString(m.renderLine(i, plain, styled))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}

func (m Model) renderSSHDetail() string {
	var sources []string
	for source, state := range m.ssh.Sources {
		sources = append(sources, source+"="+state)
	}
	sort.Strings(sources)
	lines := []string{"  Sources: " + wrapBindings(sources, max(20, m.width-13))}
	if row, ok := m.currentSSH(); ok {
		lines = append(lines, "  Machine: "+row.ID+" · "+row.State)
		var profiles []string
		for _, p := range row.Profiles {
			endpoint := p.HostName
			if endpoint == "" {
				endpoint = "endpoint unknown"
			}
			user, port := p.User, "native"
			if user == "" {
				user = "native"
			}
			if p.Port != 0 {
				port = fmt.Sprint(p.Port)
			}
			profiles = append(profiles, fmt.Sprintf("%s → %s user=%s port=%s (%s)", p.Alias, endpoint, user, port, p.State))
		}
		if len(profiles) > 0 {
			lines = append(lines, "  "+strings.Join(profiles, " · "))
		}
	}
	lines = append(lines, "  Discovery is not SSH authentication. Ctrl+O shows full source details and identity mappings.")
	for i := 1; i < len(lines); i++ {
		lines[i] = fitCell(lines[i], max(1, m.width-2))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (m Model) hasCustomSSHKey() bool {
	for _, tool := range m.actions.Tools {
		if tool.Key == "8" {
			return true
		}
	}
	return false
}

func (m Model) updateSSHKey(key string) (tea.Model, tea.Cmd, bool) {
	var action listAction
	switch key {
	case "r":
		m.beginViewLoad(ViewSSH, loadRefresh)
		return m, m.reloadSSH(), true
	case "enter", "o":
		action = listActionSSHConnect
	case "n":
		action = listActionSSHSetup
	case "c":
		action = listActionSSHDiscover
	case "p":
		action = listActionSSHProbe
	case "e", "m":
		action = listActionSSHMappings
	case "y":
		action = listActionSSHCopy
	default:
		return m, nil, false
	}
	next, command := m.runSSHAction(action)
	return next, command, true
}

func (m Model) openSSHMenu() Model {
	menu := overlayState{kind: overlayActionMenu, title: "SSH connections", selection: m.currentToken()}
	row, selected := m.currentSSH()
	if selected {
		menu.subject = row.Label
		menu.detail = row.ID
		menu.addOption(listActionSSHDetails, "full machine / source details")
	}
	if m.actions.SSH.Workflow != nil {
		if selected && len(row.Profiles) > 0 {
			menu.addOption(listActionSSHConnect, "connect through an exact SSH profile…")
			menu.addOption(listActionSSHProbe, "probe fresh SSH authentication…")
			menu.addOption(listActionSSHDiagnose, "diagnose an SSH profile…")
			menu.addOption(listActionSSHRegister, "register an SSH profile in fleet / Herdr…")
		}
		menu.addOption(listActionSSHSetup, "set up / import connections…")
		menu.addOption(listActionSSHDiscover, "discover Tailscale / LAN hosts…")
		menu.addOption(listActionSSHMappings, "adopt / link / unlink / merge machine mappings…")
	}
	if selected && m.actions.Copy != nil {
		menu.addOption(listActionSSHCopy, "copy machine data…")
	}
	if selected {
		menu.addOption(listActionSortMenu, "sort columns…")
	}
	m.overlay = menu
	return m
}

func sshRowIndependent(action listAction) bool {
	return action == listActionSSHSetup || action == listActionSSHDiscover || action == listActionSSHMappings
}

func (m Model) runSSHAction(action listAction) (tea.Model, tea.Cmd) {
	row, selected := m.currentSSH()
	if !selected && !sshRowIndependent(action) {
		return m, nil
	}
	switch action {
	case listActionSSHDetails:
		data, _ := json.MarshalIndent(struct {
			Machine SSHRow            `json:"machine"`
			Sources map[string]string `json:"sources"`
		}{row, m.ssh.Sources}, "", "  ")
		m.overlay = overlayState{kind: overlayTriageReceipt, title: "Machine and source observations", body: string(data)}
		return m, nil
	case listActionSSHCopy:
		menu := overlayState{kind: overlayActionMenu, title: "Copy machine data", selection: m.currentToken()}
		menu.addOption(listActionSSHCopyID, "machine / observation ID")
		menu.addOption(listActionSSHCopyAliases, "SSH aliases")
		menu.addOption(listActionSSHCopySummary, "safe machine summary")
		m.overlay = menu
		return m, nil
	case listActionSSHCopyID:
		return m.copyText(row.ID, "machine ID", false)
	case listActionSSHCopyAliases:
		return m.copyText(sshCell(row, "ssh"), "SSH aliases", false)
	case listActionSSHCopySummary:
		return m.copyText(sshRowSummary(row), "machine summary", false)
	}
	if m.actions.SSH.Workflow == nil {
		return m, nil
	}
	if !sshRowIndependent(action) && m.viewLoad(ViewSSH).loading {
		m.status = "Wait for the connection inventory to finish loading"
		return m, nil
	}
	names := map[listAction]string{listActionSSHConnect: "connect", listActionSSHSetup: "setup", listActionSSHDiscover: "discover", listActionSSHProbe: "probe", listActionSSHDiagnose: "diagnose", listActionSSHMappings: "mappings", listActionSSHRegister: "register"}
	name := names[action]
	workflow, err := m.actions.SSH.Workflow(m.baseContext(), SSHWorkflowRequest{Action: name, Selected: row})
	if err != nil {
		m.err = err
		return m, nil
	}
	m.err, m.status = nil, "Opening SSH "+name+"…"
	return m, tea.Exec(workflow, func(err error) tea.Msg { return afterExec(sshWorkflowMsg{result: workflow.Result(), err: err}) })
}
