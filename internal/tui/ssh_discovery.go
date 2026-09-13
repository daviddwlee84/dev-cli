package tui

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/sshactivity"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

type SSHDiscoveryRequest struct {
	Source string
	LAN    sshdiscovery.LANRequest
}
type SSHDiscoveryResult struct {
	Report     sshdiscovery.Report
	CacheError string
	CacheErr   error
}
type SSHOnboardingPlan interface{ Preview() sshflow.OnboardPreview }
type SSHTestRequest struct {
	Profiles []sshflow.ConnectionProfile
	Full     bool
}
type SSHTestProgress struct {
	ProfileID string
	Record    sshactivity.ProfileRecord
	Err       error
}
type SSHTestResult struct {
	Completed, Total int
	Canceled         bool
	// Outcomes retains one result per completed profile, even if progress delivery
	// was interrupted. Its size is bounded by the explicitly reviewed request.
	Outcomes []SSHTestProgress
}

type sshDialog struct {
	kind, title             string
	index                   int
	scroll                  int
	options                 []string
	fields                  [14]formField
	fieldCount              int
	interfaces              []sshdiscovery.InterfaceScope
	request                 SSHDiscoveryRequest
	onboarding              sshflow.OnboardRequest
	plan                    SSHOnboardingPlan
	profiles                []sshflow.ConnectionProfile
	workflow                string
	tests                   SSHTestRequest
	completed, total, found int
	body                    string
	err                     error
}
type sshEventMsg struct {
	generation uint64
	kind       string
	interfaces []sshdiscovery.InterfaceScope
	progress   sshdiscovery.Progress
	discovery  SSHDiscoveryResult
	plan       SSHOnboardingPlan
	test       SSHTestProgress
	testResult SSHTestResult
	err        error
}
type sshBackgroundMsg struct {
	at         time.Time
	generation uint64
}

func waitSSHEvent(events <-chan sshEventMsg) tea.Cmd {
	return func() tea.Msg {
		message, ok := <-events
		if !ok {
			return nil
		}
		return message
	}
}

func (m Model) openSSHDiscovery() (tea.Model, tea.Cmd) {
	m.pauseSSHBackground()
	m.sshUI.dialog = sshDialog{kind: "source", title: "Discover hosts", options: []string{"Tailscale — refresh local status", "LAN — scan an explicit local range"}}
	return m, nil
}
func (m Model) loadSSHInterfaces() (tea.Model, tea.Cmd) {
	m.sshUI.generation++
	generation := m.sshUI.generation
	m.sshUI.dialog = sshDialog{kind: "loading", title: "LAN discovery", body: "Reading local network interfaces…"}
	return m, func() tea.Msg {
		if m.actions.SSH.Interfaces == nil {
			return sshEventMsg{generation: generation, kind: "interfaces", err: errors.New("LAN interfaces are unavailable")}
		}
		interfaces, err := m.actions.SSH.Interfaces(m.baseContext())
		return sshEventMsg{generation: generation, kind: "interfaces", interfaces: interfaces, err: err}
	}
}
func sshDefaultRange(scope sshdiscovery.InterfaceScope) string {
	prefix, err := netip.ParsePrefix(scope.Prefix)
	if err != nil {
		return scope.Address
	}
	if prefix.Bits() >= 24 {
		return prefix.Masked().String()
	}
	address, err := netip.ParseAddr(scope.Address)
	if err != nil {
		return scope.Address
	}
	return netip.PrefixFrom(address, 24).Masked().String()
}
func (m Model) openSSHLANForm(scope sshdiscovery.InterfaceScope) (tea.Model, tea.Cmd) {
	d := sshDialog{kind: "lan", title: "Review LAN scan scope", request: SSHDiscoveryRequest{Source: sshdiscovery.SourceLAN}}
	d.addField("interface", "Interface", scope.Interface)
	d.addField("ranges", "IPv4 ranges", sshDefaultRange(scope))
	d.addField("ports", "Ports", "22")
	m.sshUI.dialog = d
	return m, m.focusSSHField(0)
}
func (d *sshDialog) addField(key, label, value string) {
	input := textinput.New()
	input.CharLimit = 300
	input.SetValue(value)
	input.CursorEnd()
	d.fields[d.fieldCount] = formField{key: key, label: label, input: input}
	d.fieldCount++
}
func (m *Model) focusSSHField(index int) tea.Cmd {
	d := &m.sshUI.dialog
	if d.fieldCount == 0 {
		return nil
	}
	if index < 0 {
		index = d.fieldCount - 1
	}
	if index >= d.fieldCount {
		index = 0
	}
	for i := 0; i < d.fieldCount; i++ {
		d.fields[i].input.Width = max(8, m.width-24)
		d.fields[i].input.Blur()
	}
	d.index = index
	return d.fields[index].input.Focus()
}
func (d sshDialog) value(key string) string {
	for i := 0; i < d.fieldCount; i++ {
		if d.fields[i].key == key {
			return strings.TrimSpace(d.fields[i].input.Value())
		}
	}
	return ""
}
func (m Model) startSSHDiscovery(request SSHDiscoveryRequest, background bool) (tea.Model, tea.Cmd) {
	if m.sshUI.running {
		if !m.sshUI.background {
			return m, nil
		}
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
	m.sshUI.cancel = cancel
	m.sshUI.events = events
	m.sshUI.running = true
	m.sshUI.background = background
	if background {
		m.sshUI.lastBackground = time.Now()
	} else {
		m.sshUI.dialog = sshDialog{kind: "discovering", title: "Discovering " + request.Source, request: request}
	}
	discover := m.actions.SSH.Discover
	validate := m.actions.SSH.ValidateLAN
	return m, func() tea.Msg {
		go func() {
			defer close(events)
			defer cancel()
			send := func(event sshEventMsg) bool {
				event.generation = generation
				select {
				case events <- event:
					return true
				case <-ctx.Done():
					return false
				}
			}
			if request.Source == sshdiscovery.SourceLAN && validate != nil {
				if _, err := validate(ctx, request.LAN); err != nil {
					finishSSHEvent(events, sshEventMsg{generation: generation, kind: "discovered", err: err})
					return
				}
			}
			result, err := discover(ctx, request, func(progress sshdiscovery.Progress) {
				send(sshEventMsg{kind: "discovery-progress", progress: progress})
			})
			// Completion must remain deliverable after cancellation to preserve partial results.
			finishSSHEvent(events, sshEventMsg{generation: generation, kind: "discovered", discovery: result, err: err})
		}()
		return <-events
	}
}
func (m *Model) retainSSHReport(report sshdiscovery.Report) {
	if report.Source == "" {
		return
	}
	// A partial scan replaces only candidates it actually observed. Unobserved
	// older candidates keep their original report timestamps and provenance.
	updated := map[string]bool{}
	for _, candidate := range report.Candidates {
		updated[candidateKey(candidate)] = true
	}
	reports := make([]sshdiscovery.Report, 0, len(m.sshUI.reports)+1)
	for _, old := range m.sshUI.reports {
		if old.Source != report.Source || old.Scope != report.Scope {
			reports = append(reports, old)
			continue
		}
		if report.Complete {
			continue
		}
		remaining := old
		remaining.Candidates = nil
		for _, candidate := range old.Candidates {
			if !updated[candidateKey(candidate)] {
				remaining.Candidates = append(remaining.Candidates, candidate)
			}
		}
		if len(remaining.Candidates) > 0 {
			reports = append(reports, remaining)
		}
	}
	reports = append(reports, report)
	m.sshUI.reports = reports
}

func (m Model) applySSHEvent(msg sshEventMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.sshUI.generation {
		return m, nil
	}
	switch msg.kind {
	case "interfaces":
		if msg.err != nil || len(msg.interfaces) == 0 {
			m.sshUI.dialog = sshDialog{kind: "message", title: "LAN discovery", body: "No available on-link IPv4 interface. Connect to a local network, then retry discovery.", err: msg.err}
			return m, nil
		}
		opts := make([]string, len(msg.interfaces))
		for i, s := range msg.interfaces {
			opts[i] = s.Interface + "  " + s.Address + "  " + s.Prefix
		}
		m.sshUI.dialog = sshDialog{kind: "interfaces", title: "Choose LAN interface", interfaces: msg.interfaces, options: opts}
		return m, nil
	case "discovery-progress":
		if !m.sshUI.background {
			m.sshUI.dialog.completed = msg.progress.Completed
			m.sshUI.dialog.total = msg.progress.Total
			if msg.progress.Candidate != nil {
				m.sshUI.dialog.found++
				m.sshUI.dialog.body += fmt.Sprintf("%s %v\n", msg.progress.Candidate.Name, msg.progress.Candidate.Addresses)
			}
		}
		return m, waitSSHEvent(m.sshUI.events)
	case "discovered":
		background := m.sshUI.background
		m.sshUI.running = false
		m.sshUI.background = false
		m.sshUI.cancel = nil
		report := msg.discovery.Report
		if !background && msg.err != nil && report.Source == "" && m.sshUI.dialog.request.Source == "lan" {
			request := m.sshUI.dialog.request
			d := sshDialog{kind: "lan", title: "Review LAN scan scope", request: request, err: msg.err}
			d.addField("interface", "Interface", request.LAN.Interface)
			d.addField("ranges", "IPv4 ranges", strings.Join(request.LAN.Ranges, ", "))
			ports := []string{}
			for _, port := range request.LAN.Ports {
				ports = append(ports, strconv.Itoa(port))
			}
			d.addField("ports", "Ports", strings.Join(ports, ", "))
			m.sshUI.dialog = d
			return m, m.focusSSHField(1)
		}
		m.retainSSHReport(report)
		if !background {
			m.err = nil
			if !m.sshUI.onlyDiscovery {
				m.sshUI.savedFilter = m.filter
				m.sshUI.savedSelection = m.currentToken()
			}
			m.sshUI.onlyDiscovery = true
			m.sshUI.discoveryKeys = map[string]bool{}
			for _, c := range report.Candidates {
				m.sshUI.discoveryKeys[candidateKey(c)] = true
			}
			m.filter = ""
			m.sshCursor = 0
			m.sshUI.dialog = sshDialog{}
			m.status = fmt.Sprintf("%d discoveries · Enter adds a connection · Esc shows all", len(report.Candidates))
			if guidance := sshdiscovery.LANGuidance(report, runtime.GOOS); guidance != "" {
				m.status = guidance + " · Esc shows all"
			}
			if errors.Is(msg.err, context.Canceled) {
				m.status = "Scan canceled; completed observations retained · Esc shows all"
			} else if msg.err != nil {
				m.status = "Discovery incomplete; inspect the problem and retry the selected scope"
			}
		}
		m.ssh = m.withSessionDiscovery(m.ssh)
		if msg.err != nil {
			m.err = msg.err
			m.viewErrors[ViewSSH] = msg.err
		}
		if msg.discovery.CacheError != "" || msg.discovery.CacheErr != nil {
			if msg.discovery.CacheErr != nil {
				m.err = fmt.Errorf("discovery retained for this session only: %w", msg.discovery.CacheErr)
			} else {
				m.err = fmt.Errorf("discovery retained for this session only: %s", msg.discovery.CacheError)
			}
			m.viewErrors[ViewSSH] = m.err
		}
		if m.actions.SSH.LoadWithReports != nil {
			m.beginViewLoad(ViewSSH, loadAction)
			return m, m.reloadSSH()
		}
		cmd := m.scheduleSSHBackground()
		return m, cmd
	case "prepared":
		if msg.err != nil {
			m.sshUI.dialog.kind = "onboard"
			m.sshUI.dialog.err = msg.err
			return m, m.focusSSHField(m.sshUI.dialog.index)
		}
		m.sshUI.dialog.kind = "review"
		m.sshUI.dialog.plan = msg.plan
		m.sshUI.dialog.body = sshOnboardPreview(msg.plan.Preview())
		return m, nil
	case "test-progress":
		activity := make(map[string]sshactivity.ProfileRecord, len(m.sshUI.activity)+1)
		for k, v := range m.sshUI.activity {
			activity[k] = v
		}
		record := mergeSSHTestActivity(activity[msg.test.ProfileID], msg.test.Record)
		if record.ProfileID == "" {
			record.ProfileID = msg.test.ProfileID
		}
		activity[msg.test.ProfileID] = record
		m.sshUI.activity = activity
		m.sshUI.dialog.completed++
		m.sshUI.dialog.body += m.sshProfileName(msg.test.ProfileID) + ": "
		if msg.test.Err != nil {
			m.sshUI.dialog.body += msg.test.Err.Error()
		} else if msg.test.Record.LastTest != nil {
			m.sshUI.dialog.body += msg.test.Record.LastTest.Status
		}
		m.sshUI.dialog.body += "\n"
		return m, waitSSHEvent(m.sshUI.events)
	case "tested":
		if len(msg.testResult.Outcomes) > 0 {
			activity := make(map[string]sshactivity.ProfileRecord, len(m.sshUI.activity)+len(msg.testResult.Outcomes))
			for id, record := range m.sshUI.activity {
				activity[id] = record
			}
			var lines []string
			for _, outcome := range msg.testResult.Outcomes {
				record := mergeSSHTestActivity(activity[outcome.ProfileID], outcome.Record)
				if record.ProfileID == "" {
					record.ProfileID = outcome.ProfileID
				}
				activity[outcome.ProfileID] = record
				line := m.sshProfileName(outcome.ProfileID) + ": "
				if outcome.Err != nil {
					line += outcome.Err.Error()
				} else if outcome.Record.LastTest != nil {
					line += outcome.Record.LastTest.Status
				}
				lines = append(lines, line)
			}
			m.sshUI.activity = activity
			m.sshUI.dialog.body = strings.Join(lines, "\n")
			m.sshUI.dialog.completed = msg.testResult.Completed
		}
		m.err = msg.err
		m.sshUI.running = false
		m.sshUI.cancel = nil
		m.sshUI.dialog.kind = "message"
		m.sshUI.dialog.title = "SSH test results"
		m.sshUI.dialog.err = msg.err
		if msg.testResult.Canceled {
			m.sshUI.dialog.body = "Canceled; completed results retained.\n" + m.sshUI.dialog.body
		}
		if m.actions.SSH.Load != nil && m.actions.SSH.LoadActivity != nil {
			m.beginViewLoad(ViewSSH, loadAction)
			return m, m.reloadSSH()
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) scheduleSSHBackground() tea.Cmd {
	if m.view != ViewSSH || !m.actions.SSH.BackgroundRefresh || m.actions.SSH.Discover == nil || m.sshUI.running || m.sshUI.backgroundScheduled || m.sshUI.dialog.kind != "" {
		return nil
	}
	// Read-only freshness state controls the first refresh; later attempts are bounded by TTL.
	delay := sshdiscovery.CacheTTL
	if m.sshUI.lastBackground.IsZero() {
		if m.ssh.Sources["tailscale"] != "ready" {
			delay = time.Millisecond
		}
	} else {
		delay = max(time.Millisecond, sshdiscovery.CacheTTL-time.Since(m.sshUI.lastBackground))
	}
	m.sshUI.backgroundScheduled = true
	m.sshUI.backgroundGeneration++
	generation := m.sshUI.backgroundGeneration
	return tea.Tick(delay, func(at time.Time) tea.Msg { return sshBackgroundMsg{at: at, generation: generation} })
}
func (m Model) applySSHBackground(msg sshBackgroundMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.sshUI.backgroundGeneration {
		return m, nil
	}
	m.sshUI.backgroundScheduled = false
	if m.view != ViewSSH || !m.actions.SSH.BackgroundRefresh || m.actions.SSH.Discover == nil || m.sshUI.running || m.sshUI.dialog.kind != "" {
		return m, nil
	}
	if !m.sshUI.lastBackground.IsZero() && msg.at.Sub(m.sshUI.lastBackground) < sshdiscovery.CacheTTL {
		return m, nil
	}
	return m.startSSHDiscovery(SSHDiscoveryRequest{Source: sshdiscovery.SourceTailscale}, true)
}

func (m Model) openSSHOnboarding(row SSHRow) (tea.Model, tea.Cmd) {
	m.pauseSSHBackground()
	request := sshflow.OnboardRequest{Port: 22, Auth: "config", RemoteOS: "posix", MachineID: row.MachineID}
	candidates := append(slices.Clone(row.LAN), row.Tailscale...)
	if len(candidates) > 0 {
		c := candidates[0]
		request.Candidate = &c
		request.Alias = c.Name
		if request.Alias == "" {
			request.Alias = "host"
		}
		request.Port = c.Port
		if request.Port == 0 {
			request.Port = 22
		}
		if len(c.Addresses) > 0 {
			request.HostName = c.Addresses[0]
		}
		if c.OS == "windows" {
			request.RemoteOS = "windows"
		}
		request.Report = m.sshCandidateReport(c)

		if len(candidates) > 1 || len(c.Addresses) > 1 {
			options := []string{}
			for _, candidate := range candidates {
				for _, address := range candidate.Addresses {
					options = append(options, fmt.Sprintf("%s:%d (%s)", address, candidate.Port, candidate.Source))
				}
			}
			// The exact observed candidates remain in the request/report picker state.
			m.sshUI.dialog = sshDialog{kind: "endpoints", title: "Choose observed endpoint", onboarding: request, options: options, body: sshRowSummary(row)}
			return m, nil
		}
	}
	return m.openSSHOnboardingForm(request)
}
func (m Model) openSSHOnboardingForm(request sshflow.OnboardRequest) (tea.Model, tea.Cmd) {
	d := sshDialog{kind: "onboard", title: "Add SSH connection", onboarding: request}
	d.addField("alias", "Alias", request.Alias)
	d.addField("host", "Host", request.HostName)
	d.addField("user", "Remote user", request.User)
	d.addField("port", "Port", strconv.Itoa(request.Port))
	d.addField("auth", "Authentication", request.Auth)
	d.addField("fleet", "Fleet", "no")
	d.addField("herdr", "Herdr", "no")
	d.addField("os", "Remote OS", request.RemoteOS)
	d.addField("key", "Key path", "")
	d.addField("generate", "Generate key", "no")
	m.sshUI.dialog = d
	return m, m.focusSSHField(0)
}
func (m Model) prepareSSHOnboarding() (tea.Model, tea.Cmd) {
	d := &m.sshUI.dialog
	r := d.onboarding
	r.Alias = d.value("alias")
	r.HostName = d.value("host")
	r.User = d.value("user")
	port, err := strconv.Atoi(d.value("port"))
	if err != nil || port < 1 || port > 65535 {
		d.err = errors.New("Port must be 1–65535")
		return m, nil
	}
	r.Port = port
	r.Auth = d.value("auth")
	r.RemoteOS = d.value("os")
	r.KeyPath = d.value("key")
	r.GenerateKey = d.value("generate") == "yes"
	fleet, herdr := d.value("fleet") == "yes", d.value("herdr") == "yes"
	r.To = ""
	if fleet {
		r.To = "fleet"
	}
	if herdr {
		r.To = "herdr"
	}
	if fleet && herdr {
		r.To = "both"
	}
	if r.Auth != "key" && (r.KeyPath != "" || r.GenerateKey) {
		d.err = errors.New("Key selection or generation requires key authentication")
		return m, nil
	}
	if r.Auth == "key" && r.KeyPath == "" {
		d.err = errors.New("Enter an existing key path or a new destination key path")
		return m, nil
	}
	if r.To != "" && r.Auth == "config" {
		d.err = errors.New("Fleet and Herdr require existing authentication or an explicitly selected key")
		return m, nil
	}
	d.onboarding = r
	d.kind = "preparing"
	d.err = nil
	m.sshUI.generation++
	generation := m.sshUI.generation
	return m, func() tea.Msg {
		plan, err := m.actions.SSH.PrepareOnboarding(m.baseContext(), r)
		return sshEventMsg{generation: generation, kind: "prepared", plan: plan, err: err}
	}
}
func (m Model) applySSHOnboarding() (tea.Model, tea.Cmd) {
	d := m.sshUI.dialog
	if m.actions.SSH.Workflow == nil {
		m.sshUI.dialog.err = errors.New("SSH setup apply is unavailable in this session")
		return m, nil
	}
	workflow, err := m.actions.SSH.Workflow(m.baseContext(), SSHWorkflowRequest{Action: "onboard", Onboarding: d.plan})
	if err != nil {
		m.sshUI.dialog.err = err
		return m, nil
	}
	m.sshUI.selectAlias = d.onboarding.Alias
	m.sshUI.dialog = sshDialog{}
	return m, tea.Exec(workflow, func(err error) tea.Msg { return afterExec(sshWorkflowMsg{result: workflow.Result(), err: err}) })
}
func (m *Model) finishSSHOnboarding(result SSHWorkflowResult) {
	if result.Onboarding == nil {
		return
	}
	m.leaveSSHDiscovery()
	m.filter = ""
	var lines []string
	if result.Onboarding.Init != nil {
		lines = append(lines, "SSH configuration: "+string(result.Onboarding.Init.Action)+" "+result.Onboarding.Init.Path)
	}
	for _, outcome := range result.Onboarding.Outcomes {
		line := outcome.Alias + " · " + outcome.Stage + ": " + outcome.Status
		if outcome.Error != "" {
			line += " — " + outcome.Error
		}
		lines = append(lines, line)
	}
	m.sshUI.dialog = sshDialog{kind: "message", title: "Connection setup result", body: strings.Join(lines, "\n")}

}
func (m Model) openSSHProfiles(action string, row SSHRow) (tea.Model, tea.Cmd) {
	profiles := m.sshProfiles(row)
	options := make([]string, len(profiles))
	for i, p := range profiles {
		options[i] = p.Alias + " → " + p.HostName
	}
	m.sshUI.dialog = sshDialog{kind: "profiles", title: "Choose exact SSH profile", profiles: profiles, workflow: action, options: options}
	return m, nil
}

func (m *Model) pauseSSHBackground() {
	m.sshUI.backgroundGeneration++
	m.sshUI.backgroundScheduled = false
	if m.sshUI.background {
		if m.sshUI.cancel != nil {
			m.sshUI.cancel()
		}
		m.sshUI.generation++
		m.sshUI.running = false
		m.sshUI.background = false
		m.sshUI.cancel = nil
	}
}
func finishSSHEvent(events chan sshEventMsg, event sshEventMsg) {
	select {
	case events <- event:
		return
	default:
	}
	// A final report replaces one queued progress update if the consumer has
	// stopped, so canceling the program never leaves a blocked producer.
	select {
	case <-events:
	default:
	}
	select {
	case events <- event:
	default:
	}
}
func sshOnboardPreview(preview sshflow.OnboardPreview) string {
	var lines []string
	for _, target := range preview.Targets {
		lines = append(lines, fmt.Sprintf("%s → %s@%s:%d", target.Alias, target.User, target.HostName, target.Port))
		lines = append(lines, "Save alias to the managed SSH configuration.")
		switch target.Auth {
		case "config":
			lines = append(lines, "Authentication: save configuration only")
		case "existing":
			lines = append(lines, "Authentication: verify existing native SSH access")
		case "key":
			lines = append(lines, "Authentication: bootstrap the selected public key")
		}
		if target.To != "" {
			lines = append(lines, "Register after authentication: "+target.To+" ("+target.RemoteOS+")")
		}
	}
	if preview.Init.Path != "" {
		lines = append(lines, "SSH initialization: "+string(preview.Init.Action)+" "+preview.Init.Path, "Managed aliases: "+preview.Init.ManagedDir)
	}
	lines = append(lines, preview.Notes...)
	return strings.Join(lines, "\n")
}
