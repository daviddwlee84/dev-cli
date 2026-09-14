package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/sshactivity"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

type SSHInventory = sshflow.MachineInventory
type SSHRow = sshflow.MachineRow

// SSHActions separates passive inventory from explicit foreground actions.
// Its loader reads local configuration and caches. Discovery and authentication
// have distinct callbacks and never run as a side effect of rendering.
type SSHActions struct {
	Load              func(context.Context) (SSHInventory, error)
	LoadActivity      func(context.Context) (map[string]sshactivity.ProfileRecord, error)
	LoadReports       func(context.Context) ([]sshdiscovery.Report, error)
	Interfaces        func(context.Context) ([]sshdiscovery.InterfaceScope, error)
	ValidateLAN       func(context.Context, sshdiscovery.LANRequest) (string, error)
	Discover          func(context.Context, SSHDiscoveryRequest, func(sshdiscovery.Progress)) (SSHDiscoveryResult, error)
	LoadWithReports   func(context.Context, []sshdiscovery.Report) (SSHInventory, error)
	PrepareOnboarding func(context.Context, sshflow.OnboardRequest) (SSHOnboardingPlan, error)
	Test              func(context.Context, SSHTestRequest, func(SSHTestProgress)) (SSHTestResult, error)
	BackgroundRefresh bool
	Workflow          func(context.Context, SSHWorkflowRequest) (SSHWorkflow, error)
	// ListKeys returns local key choices plus a generate choice for the alias.
	ListKeys func(ctx context.Context, alias string) ([]SSHKeyChoice, error)
}

// SSHKeyChoice is one key picker entry; Generate means Path is a new key destination.
type SSHKeyChoice struct {
	Label, Description, Path string
	Generate                 bool
}

type SSHWorkflowRequest struct {
	Action     string
	Selected   SSHRow
	Profile    *sshflow.ConnectionProfile
	Onboarding SSHOnboardingPlan
}
type SSHWorkflowResult struct {
	Status            string
	MembershipChanged bool
	Onboarding        *sshflow.OnboardExecutionResult
}
type SSHWorkflow interface {
	tea.ExecCommand
	Result() SSHWorkflowResult
}
type sshLoadedMsg struct {
	generation    uint64
	inventory     SSHInventory
	activity      map[string]sshactivity.ProfileRecord
	activityErr   error
	reports       []sshdiscovery.Report
	reportsErr    error
	reportsLoaded bool
	err           error
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
		var inventory SSHInventory
		var err error
		if m.actions.SSH.LoadWithReports != nil && len(m.sshUI.reports) > 0 {
			inventory, err = m.actions.SSH.LoadWithReports(ctx, m.sshUI.reports)
		} else {
			inventory, err = m.actions.SSH.Load(ctx)
		}
		var activity map[string]sshactivity.ProfileRecord
		var activityErr error
		if m.actions.SSH.LoadActivity != nil {
			activity, activityErr = m.actions.SSH.LoadActivity(ctx)
		}
		var reports []sshdiscovery.Report
		var reportsErr error
		if m.actions.SSH.LoadReports != nil {
			reports, reportsErr = m.actions.SSH.LoadReports(ctx)
		}
		outcome := perftrace.OutcomeSuccess
		if err != nil {
			outcome = perftrace.OutcomeFailed
		}
		finish(outcome)
		return sshLoadedMsg{generation: generation, inventory: inventory, activity: activity, activityErr: activityErr, reports: reports, reportsErr: reportsErr, reportsLoaded: m.actions.SSH.LoadReports != nil, err: err}
	}
}

func (m Model) applySSHLoad(message sshLoadedMsg) (tea.Model, tea.Cmd) {
	token := m.currentToken()
	usable := message.err == nil || message.inventory.Kind != "" || len(message.inventory.Machines) > 0
	sourceErr := errors.Join(message.err, message.activityErr, message.reportsErr)
	if !m.applyViewResult(ViewSSH, message.generation, usable, perftrace.SourceLive, resultFreshness(sourceErr), len(message.inventory.Machines), sourceErr, message.err == nil) {
		return m, nil
	}
	if message.reportsLoaded && (message.reportsErr == nil || message.reports != nil) {
		m.sshUI.cachedReports = message.reports
	}
	if usable {
		m.ssh = m.withSessionDiscovery(message.inventory)
		if message.activity != nil {
			activity := make(map[string]sshactivity.ProfileRecord, len(m.sshUI.activity)+len(message.activity))
			for id, record := range m.sshUI.activity {
				activity[id] = record
			}
			for id, record := range message.activity {
				activity[id] = mergeSSHTestActivity(activity[id], record)
			}
			m.sshUI.activity = activity
		}
		m.setViewStatus(ViewSSH, "Space profiles · c discover · p test · n add connection")
	}
	if m.sshUI.selectAlias != "" {
		for _, row := range m.ssh.Machines {
			for _, profile := range row.Profiles {
				if profile.Alias == m.sshUI.selectAlias {
					m.sshUI.expanded = map[string]bool{row.ID: true}
					token = selectionToken{view: ViewSSH, key: "profile:" + profile.ID}
					m.sshUI.selectAlias = ""
				}
			}
		}
	}
	m.sshUI.selectAlias = ""
	if m.view == ViewSSH {
		if !m.selectToken(token) {
			m.setAt(m.at())
		}
	}
	cmd := m.scheduleSSHBackground()
	return m, cmd
}

// visibleSSH retains the machine API for existing action adapters; tree entry
// identity and exact profile selection live in the presentation layer.
func (m Model) visibleSSH() []SSHRow {
	entries := m.visibleSSHEntries()
	rows := make([]SSHRow, len(entries))
	for i, e := range entries {
		rows[i] = e.machine
	}
	return rows
}
func (m Model) currentSSH() (SSHRow, bool) {
	e, ok := m.currentSSHEntry()
	return e.machine, ok
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
	rows := m.visibleSSHEntries()
	if len(rows) == 0 {
		if m.viewLoad(ViewSSH).loading {
			return "  Loading local connections and discovery cache…\n"
		}
		if m.sshUI.onlyDiscovery {
			return "  No endpoints discovered. c adjusts scope; Esc returns to all connections.\n"
		}
		return "  No matching machines. Press n to add a connection or c to discover hosts.\n"
	}
	cols := m.sshColumns()
	line := func(values []string) string {
		for i := range values {
			values[i] = sshPad(values[i], cols[i].width)
		}
		return strings.Join(values, "  ")
	}
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = strings.ToUpper(c.name)
	}
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(line(header)) + "\n")
	from, to := m.window(len(rows))
	for i := from; i < to; i++ {
		vals := make([]string, len(cols))
		for j, c := range cols {
			vals[j] = m.sshEntryCell(rows[i], c.name)
		}
		plain := line(vals)
		styled := plain
		if len(rows[i].machine.Profiles) == 0 {
			styled = styleDim.Render(plain)
		}
		b.WriteString(m.renderLine(i, plain, styled))
	}
	b.WriteString(m.scrollNote(len(rows), from, to))
	return b.String()
}

type sshColumn struct {
	name  string
	width int
}

func (m Model) sshColumns() []sshColumn {
	available := max(1, m.width-4)
	columns := []sshColumn{{"connection", 0}, {"check", 9}}
	if m.width >= 40 {
		columns = append(columns, sshColumn{"used", 10})
	}
	if m.width >= 75 {
		columns = []sshColumn{{"connection", 0}, {"endpoint", max(18, available/3)}, {"check", 12}, {"used", 10}}
	}
	if m.width >= 110 {
		columns = []sshColumn{{"connection", 0}, {"endpoint", max(20, available/3)}, {"sources", 9}, {"check", 12}, {"used", 10}}
	}
	remaining := available - 2*(len(columns)-1)
	for i := 1; i < len(columns); i++ {
		remaining -= columns[i].width
	}
	columns[0].width = max(1, remaining)
	return columns
}
func sshPad(value string, width int) string {
	value = fitCell(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
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
		lines = append(lines, m.sshDiscoveryDates(row)...)
		entry, _ := m.currentSSHEntry()
		profiles := m.sshProfiles(row)
		if len(profiles) > 1 && entry.profile == nil {
			tested, auth, network, last := m.sshCheckSummary(row)
			if tested > 0 {
				lines = append(lines, fmt.Sprintf("  Historical checks: %d SSH authenticated · %d network only · %d/%d tested · %s", auth, network, tested, len(row.Profiles), sshAge(last)))
			}
			profiles = profiles[:1]
			lines = append(lines, fmt.Sprintf("  %d profiles · Space expands exact aliases", len(row.Profiles)))
		}
		if entry.profile != nil {
			profiles = []sshflow.ConnectionProfile{*entry.profile}
		}
		for _, p := range profiles {
			host := p.HostName
			if host == "" {
				host = "native config"
			}
			user := p.User
			if user == "" {
				user = "native"
			}
			port := "native"
			if p.Port > 0 {
				port = fmt.Sprint(p.Port)
			}
			lines = append(lines, fmt.Sprintf("  %s → %s user=%s port=%s (%s)", p.Alias, host, user, port, p.State))
			if entry.profile != nil {
				record := m.sshUI.activity[p.ID]
				if test := record.LastTest; test != nil {
					freshness := "historical; current route not revalidated"
					if test.Fingerprint != p.Fingerprint {
						freshness = "stale: configuration changed"
					}
					lines = append(lines, "  Last check: "+test.ObservedAt.Format("2006-01-02 15:04 MST")+" · "+test.Mode+" · "+freshness)
					var stages []string
					for _, stage := range test.Stages {
						timing := fmt.Sprintf("%dms elapsed", stage.ElapsedMS)
						if stage.RoundTripMS != nil {
							prefix := ""
							if stage.RoundTripUpperBound {
								prefix = "<"
							}
							timing = fmt.Sprintf("%s%.2fms RTT", prefix, *stage.RoundTripMS)
						}
						stages = append(stages, fmt.Sprintf("%s=%s (%s)", stage.Name, stage.State, timing))
					}
					lines = append(lines, "  "+strings.Join(stages, " · "))
				}
			}
		}

	}
	lines = append(lines, "  S SSH · T Tailscale · L LAN · F Fleet · H Herdr. Discovery is not authentication.")
	if m.sshUI.onlyDiscovery {
		lines = append(lines, "  This discovery · Enter adds the selected endpoint · Esc shows all connections")
	}
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
	case " ":
		m.toggleSSHEntry()
		return m, nil, true
	case "esc":
		if m.sshUI.onlyDiscovery {
			m.leaveSSHDiscovery()
			return m, nil, true
		}
		return m, nil, false
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
	if m.actions.SSH.Workflow != nil || m.actions.SSH.Discover != nil {
		if selected && len(row.Profiles) > 0 {
			menu.addOption(listActionSSHConnect, "connect through an exact SSH profile…")
			menu.addOption(listActionSSHProbe, "test connectivity / SSH authentication…")
			menu.addOption(listActionSSHDiagnose, "diagnose an SSH profile…")
			if m.ssh.Sources["registry"] != "unavailable" {
				menu.addOption(listActionSSHRegister, "register an SSH profile in fleet / Herdr…")
			}
		}
		if selected && len(row.LAN)+len(row.Tailscale) > 0 && m.actions.SSH.PrepareOnboarding != nil {
			menu.addOption(listActionSSHSetupTarget, "set up this discovered target…")
		}
		if selected && len(row.Profiles) > 0 && m.actions.SSH.PrepareOnboarding != nil {
			menu.addOption(listActionSSHInstallKey, "set up / install an SSH key…")
		}
		menu.addOption(listActionSSHSetup, "set up / import connections…")
		menu.addOption(listActionSSHDiscover, "discover Tailscale / LAN hosts…")
		if m.ssh.Sources["registry"] != "unavailable" {
			menu.addOption(listActionSSHMappings, "adopt / link / unlink / merge machine mappings…")
		}
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
	if (action == listActionSSHMappings || action == listActionSSHRegister) && m.ssh.Sources["registry"] == "unavailable" {
		m.status = "Machine mappings unavailable · Ctrl+O → problems and suggested actions"
		return m, nil
	}
	if !selected && !sshRowIndependent(action) {
		return m, nil
	}
	switch action {
	case listActionSSHDetails:
		data, _ := json.MarshalIndent(struct {
			Machine  SSHRow                               `json:"machine"`
			Sources  map[string]string                    `json:"sources"`
			Activity map[string]sshactivity.ProfileRecord `json:"activity"`
		}{row, m.ssh.Sources, m.sshMachineActivity(row)}, "", "  ")
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
	if action == listActionSSHDiscover && m.actions.SSH.Discover != nil {
		return m.openSSHDiscovery()
	}
	if action == listActionSSHSetup && m.actions.SSH.PrepareOnboarding != nil {
		return m.openSSHOnboarding(SSHRow{})
	}
	if action == listActionSSHConnect && selected && len(row.Profiles) == 0 && m.actions.SSH.PrepareOnboarding != nil {
		return m.openSSHOnboarding(row)
	}
	if action == listActionSSHSetupTarget && m.actions.SSH.PrepareOnboarding != nil {
		return m.openSSHOnboarding(row)
	}
	if action == listActionSSHInstallKey && len(row.Profiles) > 0 && m.actions.SSH.PrepareOnboarding != nil {
		if entry, ok := m.currentSSHEntry(); ok && entry.profile != nil {
			return m.openSSHKeyForm(*entry.profile)
		}
		if len(row.Profiles) == 1 {
			return m.openSSHKeyForm(row.Profiles[0])
		}
		return m.openSSHProfiles("install-key", row)
	}
	if (action == listActionSSHProbe || action == listActionSSHDiagnose) && m.actions.SSH.Test != nil {
		return m.openSSHTests()
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
	request := SSHWorkflowRequest{Action: name, Selected: row}
	if entry, ok := m.currentSSHEntry(); ok {
		request.Profile = entry.profile
	}
	if request.Profile == nil && len(row.Profiles) == 1 {
		profile := row.Profiles[0]
		request.Profile = &profile
	}
	if request.Profile == nil && len(row.Profiles) > 1 && action != listActionSSHMappings {
		return m.openSSHProfiles(name, row)
	}
	workflow, err := m.actions.SSH.Workflow(m.baseContext(), request)
	if err != nil {
		m.err = err
		return m, nil
	}
	m.err, m.status = nil, "Opening SSH "+name+"…"
	return m, tea.Exec(workflow, func(err error) tea.Msg { return afterExec(sshWorkflowMsg{result: workflow.Result(), err: err}) })
}

func (m Model) sshMachineActivity(row SSHRow) map[string]sshactivity.ProfileRecord {
	out := make(map[string]sshactivity.ProfileRecord, len(row.Profiles))
	for _, profile := range row.Profiles {
		if record, ok := m.sshUI.activity[profile.ID]; ok {
			out[profile.ID] = record
		}
	}
	return out
}
