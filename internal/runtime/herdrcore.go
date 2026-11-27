package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"sort"
	"strings"
	"time"
)

// HerdrCore drives herdr-core (`hc`), the persistent terminal server behind
// the official Herdr Desktop app, and optionally Herdr Desktop itself
// (`herdr-app`) for the spaces a person sees.
//
// It is a different product from the classic `herdr` multiplexer: terminals
// are addressed as `t7`, the caller's address is HC_TERMINAL_ID, and spaces
// and tabs belong to the desktop window rather than to the server. dev keeps
// the same division of responsibility as with every runtime: it never creates
// or manages Git worktrees here, it only surfaces a checkout path the calling
// flow already selected.
//
// Remote machine terminals (`machine/t7`) are deliberately ignored. Their
// paths live on another host, so they can neither cover nor occupy a local
// checkout.
type HerdrCore struct {
	hcBin  string
	appBin string
	// Test seams for the hc and herdr-app JSON protocols and the inherited
	// caller environment.
	runHC  func(context.Context, ...string) ([]byte, error)
	runApp func(context.Context, ...string) ([]byte, error)
	getenv func(string) string
	// interactive attaches a client to the caller's terminal.
	interactive func(context.Context, string, ...string) error
}

// Labels dev writes on terminals it opened. Close refuses surfaces that lack
// this proof, so a person's own spaces and terminals are never ended by dev.
const (
	herdrCoreOwnedLabel = "dev.owned"
	herdrCoreLabelSpace = "dev."
	herdrCoreMaxGets    = 64
)

// NewHerdrCore returns the herdr-core adapter. The server CLI installs as
// `herdr-core`; `hc` is its documented short name but is often a shell alias,
// so the real binary name is preferred.
func NewHerdrCore() *HerdrCore {
	hc := "herdr-core"
	if !haveBinary(hc) && haveBinary("hc") {
		hc = "hc"
	}
	return &HerdrCore{hcBin: hc, appBin: herdrAppBinary(), getenv: os.Getenv, interactive: runInteractive}
}

// herdrAppBinary locates the Herdr Desktop control CLI: the `herdr-app` link
// the app installs, or the bundled macOS executable.
func herdrAppBinary() string {
	if haveBinary("herdr-app") {
		return "herdr-app"
	}
	if goruntime.GOOS == "darwin" {
		bundled := "/Applications/Herdr.app/Contents/MacOS/Herdr"
		if info, err := os.Stat(bundled); err == nil && !info.IsDir() {
			return bundled
		}
	}
	return ""
}

// Name implements Runtime.
func (h *HerdrCore) Name() string { return "herdr-core" }

// Available reports whether the hc binary exists and its server answers.
// A stopped server must degrade to the next backend instead of failing every
// command, and `server status` never starts one.
func (h *HerdrCore) Available() bool {
	if h.runHC == nil && !haveBinary(h.hcBin) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := h.hc(ctx, "server", "status", "--json")
	return err == nil
}

// DesktopAvailable reports whether a Herdr Desktop window is running and
// answers. Spaces exist only in that window; without it dev opens plain hc
// terminals instead.
func (h *HerdrCore) DesktopAvailable(ctx context.Context) bool {
	if h.runApp == nil && h.appBin == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := h.app(ctx, "status")
	return err == nil
}

func (h *HerdrCore) hc(ctx context.Context, args ...string) ([]byte, error) {
	if h.runHC != nil {
		return h.runHC(ctx, args...)
	}
	return runJSONCommand(ctx, "hc", h.hcBin, args...)
}

func (h *HerdrCore) app(ctx context.Context, args ...string) ([]byte, error) {
	if h.runApp != nil {
		return h.runApp(ctx, args...)
	}
	if h.appBin == "" {
		return nil, errors.New("herdr-app: Herdr Desktop is not installed")
	}
	return runJSONCommand(ctx, "herdr-app", h.appBin, args...)
}

func runJSONCommand(ctx context.Context, display, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", display, strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// hcTerminal is one row of `hc ls --json` or the document of `hc get --json`.
// `ls` reports status as a string; `get` reports an object with the
// recognized app and its state.
type hcTerminal struct {
	Address    string            `json:"address"`
	Agent      string            `json:"agent"`
	CWD        string            `json:"cwd"`
	Name       string            `json:"name"`
	State      string            `json:"state"`
	Where      string            `json:"where"`
	Labels     map[string]string `json:"labels"`
	Status     json.RawMessage   `json:"status"`
	Foreground *struct {
		CWD  string `json:"cwd"`
		Name string `json:"name"`
	} `json:"foreground"`
}

func (t hcTerminal) local() bool { return t.Address != "" && !strings.Contains(t.Address, "/") }

// status returns the recognized agent app and its state. A terminal running
// a plain shell has neither.
func (t hcTerminal) status() (app, state string) {
	raw := bytes.TrimSpace(t.Status)
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return t.Agent, s
	}
	var obj struct {
		App   string `json:"app"`
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if obj.App == "" {
			obj.App = t.Agent
		}
		return obj.App, obj.State
	}
	return "", ""
}

func (t hcTerminal) dirs() []string {
	var out []string
	if t.Foreground != nil && t.Foreground.CWD != "" {
		out = append(out, t.Foreground.CWD)
	}
	if t.CWD != "" && (len(out) == 0 || out[0] != t.CWD) {
		out = append(out, t.CWD)
	}
	return out
}

type hcList struct {
	Terminals *[]hcTerminal `json:"terminals"`
}

// appSpace is one space of `herdr-app ls`.
type appSpace struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Folder  string  `json:"folder"`
	Machine *string `json:"machine"`
	Active  bool    `json:"active"`
	Tabs    []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Active    bool   `json:"active"`
		Terminals []struct {
			Terminal string  `json:"terminal"`
			Pane     string  `json:"pane"`
			Status   *string `json:"status"`
			App      *string `json:"app"`
		} `json:"terminals"`
	} `json:"tabs"`
}

func (s appSpace) local() bool { return s.Machine == nil || *s.Machine == "" }

func (s appSpace) terminals() []string {
	var out []string
	for _, tab := range s.Tabs {
		for _, t := range tab.Terminals {
			out = append(out, t.Terminal)
		}
	}
	return out
}

type appLayout struct {
	Spaces *[]appSpace `json:"spaces"`
}

// terminals returns the live local terminals with their foreground cwd.
// `hc ls` reports only the shell's cwd, which is not where an agent started
// from that shell is working, so each local terminal is read with `hc get`.
func (h *HerdrCore) terminals(ctx context.Context) ([]hcTerminal, error) {
	raw, err := h.hc(ctx, "ls", "--json")
	if err != nil {
		return nil, err
	}
	var list hcList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("hc ls: unexpected output: %w", err)
	}
	if list.Terminals == nil {
		return nil, errors.New("hc ls: response has no terminals array")
	}
	var out []hcTerminal
	for _, t := range *list.Terminals {
		if !t.local() || (t.State != "" && t.State != "live") {
			continue
		}
		out = append(out, t)
	}
	if len(out) > herdrCoreMaxGets {
		return nil, fmt.Errorf("hc ls: %d local terminals exceed the %d dev observes", len(out), herdrCoreMaxGets)
	}
	for i := range out {
		detail, err := h.terminal(ctx, out[i].Address)
		if err != nil {
			return nil, err
		}
		out[i] = detail
	}
	return out, nil
}

func (h *HerdrCore) terminal(ctx context.Context, address string) (hcTerminal, error) {
	raw, err := h.hc(ctx, "get", address, "--json")
	if err != nil {
		return hcTerminal{}, err
	}
	var t hcTerminal
	if err := json.Unmarshal(raw, &t); err != nil {
		return hcTerminal{}, fmt.Errorf("hc get %s: unexpected output: %w", address, err)
	}
	if t.Address == "" {
		return hcTerminal{}, fmt.Errorf("hc get %s: response has no address", address)
	}
	return t, nil
}

// spaces returns Herdr Desktop's local spaces, or nil when no window runs.
func (h *HerdrCore) spaces(ctx context.Context) ([]appSpace, error) {
	if !h.DesktopAvailable(ctx) {
		return nil, nil
	}
	raw, err := h.app(ctx, "ls")
	if err != nil {
		return nil, err
	}
	var layout appLayout
	if err := json.Unmarshal(raw, &layout); err != nil {
		return nil, fmt.Errorf("herdr-app ls: unexpected output: %w", err)
	}
	if layout.Spaces == nil {
		return nil, errors.New("herdr-app ls: response has no spaces array")
	}
	var out []appSpace
	for _, s := range *layout.Spaces {
		if s.local() && s.ID != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// List implements Runtime. With Herdr Desktop running, a session is a space
// and its panes are the space's terminals. Terminals no space shows (or every
// terminal when no window runs) are sessions of their own.
func (h *HerdrCore) List(ctx context.Context) ([]Session, error) {
	terminals, err := h.terminals(ctx)
	if err != nil {
		return nil, err
	}
	spaces, err := h.spaces(ctx)
	if err != nil {
		return nil, err
	}
	byAddress := make(map[string]hcTerminal, len(terminals))
	for _, t := range terminals {
		byAddress[t.Address] = t
	}
	claimed := map[string]bool{}
	var out []Session
	for _, s := range spaces {
		session := Session{Handle: s.ID, Label: s.Name, Focused: s.Active}
		for _, tab := range s.Tabs {
			for _, ref := range tab.Terminals {
				t, ok := byAddress[ref.Terminal]
				if !ok {
					continue
				}
				claimed[t.Address] = true
				session.Panes = append(session.Panes, h.pane(t, tab.ID))
				session.Dirs = appendUnique(session.Dirs, t.dirs()...)
				if _, state := t.status(); state != "" && session.AgentStatus == "" {
					session.AgentStatus = state
				}
			}
		}
		if s.Folder != "" {
			session.Dirs = appendUnique(session.Dirs, s.Folder)
		}
		out = append(out, session)
	}
	for _, t := range terminals {
		if claimed[t.Address] {
			continue
		}
		label := t.Name
		if label == "" {
			label = t.Where
		}
		if label == "" {
			label = t.Address
		}
		_, state := t.status()
		out = append(out, Session{
			Handle: t.Address, Label: label, Dirs: t.dirs(), AgentStatus: state,
			Panes: []Pane{h.pane(t, "")},
		})
	}
	return out, nil
}

func (h *HerdrCore) pane(t hcTerminal, tab string) Pane {
	app, state := t.status()
	p := Pane{ID: t.Address, TabID: tab, TerminalID: t.Address, CWD: t.CWD, ShellCWD: t.CWD, AgentStatus: state}
	if t.Foreground != nil && t.Foreground.CWD != "" {
		p.CWD = t.Foreground.CWD
	}
	if app != "" {
		p.Agent = app
		p.AgentName = t.Name
	}
	return p
}

// CurrentPaneID implements CurrentPaneResolver from HC_TERMINAL_ID, verified
// against the live server rather than trusted as an inherited hint.
func (h *HerdrCore) CurrentPaneID(ctx context.Context) (string, error) {
	address := h.getenv("HC_TERMINAL_ID")
	if address == "" {
		return "", errors.New("not inside a herdr-core terminal (HC_TERMINAL_ID is unset)")
	}
	t, err := h.terminal(ctx, address)
	if err != nil {
		return "", err
	}
	return t.Address, nil
}

// AgentActivities implements AgentActivityLister. A terminal whose program hc
// recognizes as an agent occupies its foreground cwd whatever its state.
func (h *HerdrCore) AgentActivities(ctx context.Context) ([]AgentActivity, error) {
	terminals, err := h.terminals(ctx)
	if err != nil {
		return nil, err
	}
	var out []AgentActivity
	for _, t := range terminals {
		app, state := t.status()
		if app == "" {
			continue
		}
		dirs := t.dirs()
		if len(dirs) == 0 {
			return nil, fmt.Errorf("hc get %s: recognized agent has no cwd", t.Address)
		}
		out = append(out, AgentActivity{
			PaneID: t.Address, WorkspaceID: t.Labels["herdr.space"],
			Agent: app, Name: t.Name, Status: state, CWD: dirs[0],
		})
	}
	return out, nil
}

// Open implements Runtime. A space or terminal already covering dir is
// reused; otherwise Herdr Desktop gets a new unfocused space, or, without a
// window, hc gets a new detached shell terminal. Navigation calls Activate.
func (h *HerdrCore) Open(ctx context.Context, dir, label string) (OpenResult, error) {
	existing, err := h.List(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	desktop := h.DesktopAvailable(ctx)
	for _, s := range existing {
		// Only a surface the person can be switched to is reusable: with a
		// window running, a bare terminal no space shows is not.
		if desktop && !isSpaceHandle(s.Handle) {
			continue
		}
		if s.Covers(dir) {
			return OpenResult{Handle: s.Handle, Surface: surfaceOf(s.Handle), Opened: true}, nil
		}
	}
	if desktop {
		return h.createSpace(ctx, dir, label)
	}
	return h.createTerminal(ctx, dir, label)
}

func (h *HerdrCore) createSpace(ctx context.Context, dir, label string) (OpenResult, error) {
	before, err := h.spaces(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	known := map[string]bool{}
	for _, s := range before {
		known[s.ID] = true
	}
	args := []string{"do", "space.new", "--folder", dir, "--focus", "false"}
	if label != "" {
		args = append(args, "--name", label)
	}
	if _, err := h.app(ctx, args...); err != nil {
		return OpenResult{}, err
	}
	// The response shape of `do` is not a documented contract, so the new
	// space is identified by diffing the layout rather than by parsing it.
	after, err := h.spaces(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	for _, s := range after {
		if known[s.ID] {
			continue
		}
		if label != "" && s.Name != label {
			continue
		}
		res := OpenResult{Handle: s.ID, Surface: "space", Opened: true, Created: true}
		for _, address := range s.terminals() {
			if err := h.label(ctx, address, map[string]string{"owned": "1"}); err != nil {
				return res, err
			}
		}
		return res, nil
	}
	return OpenResult{}, fmt.Errorf("herdr-app do space.new: no new space for %s appeared", dir)
}

func (h *HerdrCore) createTerminal(ctx context.Context, dir, label string) (OpenResult, error) {
	args := []string{"run", "--bg", "--json", "--no-parent", "--outlive-parent", "--cwd", dir,
		"--label", herdrCoreOwnedLabel + "=1"}
	if label != "" {
		args = append(args, "--label", herdrCoreLabelSpace+"label="+label)
	}
	raw, err := h.hc(ctx, args...)
	if err != nil {
		return OpenResult{}, err
	}
	var res struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Address == "" {
		return OpenResult{}, fmt.Errorf("hc run: response has no terminal address")
	}
	// No RootPaneID: herdr-core offers no exact-pane launch capability.
	return OpenResult{Handle: res.Address, Surface: "terminal", Opened: true, Created: true}, nil
}

// Activate switches Herdr Desktop to a space. A bare terminal is attached only
// from outside herdr-core: attaching inside an hc terminal would nest clients.
func (h *HerdrCore) Activate(ctx context.Context, handle string) error {
	if handle == "" {
		return nil
	}
	if isSpaceHandle(handle) {
		_, err := h.app(ctx, "do", "space.select", "--id", handle)
		return err
	}
	if h.getenv("HC_TERMINAL_ID") != "" {
		return nil
	}
	return h.interactive(ctx, h.hcBin, "attach", handle)
}

// Close implements Runtime. Only surfaces dev opened are ended: every local
// terminal of a space must carry dev's ownership label, so a space a person
// added their own tabs to is refused rather than closed with their work.
func (h *HerdrCore) Close(ctx context.Context, handle string) error {
	if handle == "" {
		return nil
	}
	owned, err := h.ownedTerminals(ctx)
	if err != nil {
		return err
	}
	if !isSpaceHandle(handle) {
		if !owned[handle] {
			return fmt.Errorf("herdr-core terminal %s was not opened by dev; close it yourself", handle)
		}
		_, err := h.hc(ctx, "kill", handle)
		return err
	}
	spaces, err := h.spaces(ctx)
	if err != nil {
		return err
	}
	for _, s := range spaces {
		if s.ID != handle {
			continue
		}
		for _, address := range s.terminals() {
			if !owned[address] {
				return fmt.Errorf("herdr-core space %s holds terminal %s that dev did not open; close it yourself", handle, address)
			}
		}
		_, err := h.app(ctx, "do", "space.close", "--id", handle, "--confirmed", "true")
		return err
	}
	return nil
}

func (h *HerdrCore) ownedTerminals(ctx context.Context) (map[string]bool, error) {
	raw, err := h.hc(ctx, "ls", "--json", "--label", herdrCoreOwnedLabel+"=1")
	if err != nil {
		return nil, err
	}
	var list hcList
	if err := json.Unmarshal(raw, &list); err != nil || list.Terminals == nil {
		return nil, errors.New("hc ls --label: response has no terminals array")
	}
	out := map[string]bool{}
	for _, t := range *list.Terminals {
		out[t.Address] = true
	}
	return out, nil
}

// Annotate implements Runtime with dev-namespaced terminal labels on the
// session's terminals. An empty value removes the label.
func (h *HerdrCore) Annotate(ctx context.Context, handle string, kv map[string]string) error {
	if handle == "" || len(kv) == 0 {
		return nil
	}
	targets := []string{handle}
	if isSpaceHandle(handle) {
		spaces, err := h.spaces(ctx)
		if err != nil {
			return err
		}
		targets = nil
		for _, s := range spaces {
			if s.ID == handle {
				targets = s.terminals()
			}
		}
	}
	for _, address := range targets {
		if err := h.label(ctx, address, kv); err != nil {
			return err
		}
	}
	return nil
}

func (h *HerdrCore) label(ctx context.Context, address string, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	names := make([]string, 0, len(kv))
	for k := range kv {
		names = append(names, k)
	}
	sort.Strings(names)
	args := []string{"label", address}
	for _, k := range names {
		args = append(args, herdrCoreLabelSpace+k+"="+kv[k])
	}
	args = append(args, "--json")
	_, err := h.hc(ctx, args...)
	return err
}

// isSpaceHandle distinguishes Herdr Desktop space ids (`sp…`) from hc
// terminal addresses (`t7`).
func isSpaceHandle(handle string) bool { return strings.HasPrefix(handle, "sp") }

func surfaceOf(handle string) string {
	if isSpaceHandle(handle) {
		return "space"
	}
	return "terminal"
}

func appendUnique(dst []string, values ...string) []string {
	for _, v := range values {
		if v == "" {
			continue
		}
		dup := false
		for _, d := range dst {
			if d == v {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, v)
		}
	}
	return dst
}
