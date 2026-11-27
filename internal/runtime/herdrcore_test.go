package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeHerdrCore answers hc and herdr-app calls from in-memory state and
// records every invocation.
type fakeHerdrCore struct {
	terminals map[string]string // address -> `hc get` JSON
	order     []string
	owned     []string
	spaces    string // `herdr-app ls` JSON; empty means no window
	afterNew  string // layout after space.new
	calls     []string
	env       map[string]string
	attached  []string
}

func (f *fakeHerdrCore) core() *HerdrCore {
	return &HerdrCore{
		hcBin: "herdr-core",
		runHC: func(_ context.Context, args ...string) ([]byte, error) {
			f.calls = append(f.calls, "hc "+strings.Join(args, " "))
			switch {
			case len(args) >= 2 && args[0] == "server":
				return []byte(`{"version":"0.1.17"}`), nil
			case len(args) == 4 && args[0] == "ls" && args[2] == "--label":
				return []byte(listJSON(f.owned)), nil
			case args[0] == "ls":
				return []byte(listJSON(f.order)), nil
			case args[0] == "get":
				if doc, ok := f.terminals[args[1]]; ok {
					return []byte(doc), nil
				}
				return nil, errors.New("hc get: no such terminal")
			case args[0] == "run":
				return []byte(`{"address":"t9","agent":"zsh","state":"live"}`), nil
			case args[0] == "label", args[0] == "kill":
				return []byte(`{}`), nil
			}
			return nil, errors.New("unexpected hc call")
		},
		runApp: func(_ context.Context, args ...string) ([]byte, error) {
			f.calls = append(f.calls, "app "+strings.Join(args, " "))
			if f.spaces == "" {
				return nil, errors.New("herdr-app: window not running")
			}
			switch args[0] {
			case "status":
				return []byte(`{}`), nil
			case "ls":
				return []byte(f.spaces), nil
			case "do":
				if args[1] == "space.new" && f.afterNew != "" {
					f.spaces = f.afterNew
				}
				return []byte(`{}`), nil
			}
			return nil, errors.New("unexpected herdr-app call")
		},
		getenv: func(k string) string { return f.env[k] },
		interactive: func(_ context.Context, bin string, args ...string) error {
			f.attached = append(f.attached, bin+" "+strings.Join(args, " "))
			return nil
		},
	}
}

func listJSON(addresses []string) string {
	parts := make([]string, 0, len(addresses))
	for _, a := range addresses {
		parts = append(parts, `{"address":"`+a+`","state":"live"}`)
	}
	return `{"terminals":[` + strings.Join(parts, ",") + `]}`
}

func (f *fakeHerdrCore) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

const twoSpaces = `{"spaces":[
 {"id":"sp1","name":"dev-cli","folder":"/home/u","machine":null,"active":true,"tabs":[
  {"id":"tb1","name":"1","active":true,"terminals":[{"terminal":"t2","pane":"p2"}]},
  {"id":"tb2","name":"2","active":false,"terminals":[{"terminal":"t3","pane":"p3"}]}]},
 {"id":"box/sp9","name":"remote","folder":"/srv","machine":"box","active":false,"tabs":[
  {"id":"box/tb9","name":"1","active":true,"terminals":[{"terminal":"box/t1","pane":"box/p1"}]}]}
]}`

func standardTerminals() *fakeHerdrCore {
	return &fakeHerdrCore{
		order: []string{"t2", "t3", "t4", "box/t1"},
		terminals: map[string]string{
			"t2": `{"address":"t2","agent":"claude-code","cwd":"/home/u","state":"live","labels":{"herdr.space":"sp1"},"status":{"app":"claude-code","state":"working"},"foreground":{"cwd":"/code/dev-cli","name":"claude"}}`,
			"t3": `{"address":"t3","agent":"zsh","cwd":"/code/dev-cli","state":"live","status":null}`,
			"t4": `{"address":"t4","agent":"zsh","cwd":"/code/other","state":"live","name":"loose"}`,
		},
		spaces: twoSpaces,
		env:    map[string]string{},
	}
}

func TestHerdrCoreListGroupsSpacesAndSkipsRemoteTerminals(t *testing.T) {
	f := standardTerminals()
	sessions, err := f.core().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v, want the local space and the loose terminal", sessions)
	}
	space := sessions[0]
	if space.Handle != "sp1" || !space.Focused || len(space.Panes) != 2 || space.AgentStatus != "working" {
		t.Fatalf("space session = %+v", space)
	}
	if !space.Covers("/code/dev-cli") || space.Covers("/code/other") {
		t.Fatalf("space dirs = %v: must cover the agent's foreground cwd, not siblings", space.Dirs)
	}
	if space.Panes[0].Agent != "claude-code" || space.Panes[0].CWD != "/code/dev-cli" || space.Panes[1].Agent != "" {
		t.Fatalf("panes = %+v", space.Panes)
	}
	if loose := sessions[1]; loose.Handle != "t4" || loose.Label != "loose" || !loose.Covers("/code/other") {
		t.Fatalf("loose session = %+v", loose)
	}
	if f.called("hc get box/t1") {
		t.Fatal("remote machine terminals must not be observed as local")
	}
}

func TestHerdrCoreAgentActivitiesReportEveryRecognizedAgent(t *testing.T) {
	f := standardTerminals()
	activities, err := f.core().AgentActivities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(activities) != 1 {
		t.Fatalf("activities = %+v, want only the recognized agent", activities)
	}
	a := activities[0]
	if a.PaneID != "t2" || a.Agent != "claude-code" || a.CWD != "/code/dev-cli" || a.WorkspaceID != "sp1" || a.Status != "working" {
		t.Fatalf("activity = %+v", a)
	}
}

func TestHerdrCoreObservationFailsClosedWhenATerminalCannotBeRead(t *testing.T) {
	f := standardTerminals()
	delete(f.terminals, "t3")
	if _, err := f.core().AgentActivities(context.Background()); err == nil {
		t.Fatal("an unreadable terminal must fail agent observation rather than look unoccupied")
	}
}

func TestHerdrCoreOpenReusesCoveringSpace(t *testing.T) {
	f := standardTerminals()
	res, err := f.core().Open(context.Background(), "/code/dev-cli", "dev-cli")
	if err != nil {
		t.Fatal(err)
	}
	if res.Handle != "sp1" || res.Created || res.Surface != "space" {
		t.Fatalf("open = %+v", res)
	}
	if f.called("app do space.new") {
		t.Fatal("a covering space must be reused")
	}
}

func TestHerdrCoreOpenCreatesUnfocusedSpaceAndLabelsIt(t *testing.T) {
	f := standardTerminals()
	f.afterNew = strings.Replace(twoSpaces, `{"spaces":[`, `{"spaces":[
 {"id":"sp7","name":"fresh","folder":"/code/fresh","machine":null,"active":false,"tabs":[
  {"id":"tb7","name":"1","active":true,"terminals":[{"terminal":"t8","pane":"p8"}]}]},`, 1)
	res, err := f.core().Open(context.Background(), "/code/fresh", "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if res.Handle != "sp7" || !res.Created {
		t.Fatalf("open = %+v", res)
	}
	if !f.called("app do space.new --folder /code/fresh --focus false --name fresh") {
		t.Fatalf("calls = %v", f.calls)
	}
	if !f.called("hc label t8 dev.owned=1") {
		t.Fatalf("new space terminal must carry dev's ownership label; calls = %v", f.calls)
	}
}

func TestHerdrCoreOpenWithoutWindowStartsDetachedTerminal(t *testing.T) {
	f := standardTerminals()
	f.spaces = ""
	res, err := f.core().Open(context.Background(), "/code/fresh", "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if res.Handle != "t9" || res.Surface != "terminal" || !res.Created {
		t.Fatalf("open = %+v", res)
	}
	if !f.called("hc run --bg --json --no-parent --outlive-parent --cwd /code/fresh --label dev.owned=1 --label dev.label=fresh") {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestHerdrCoreActivateNeverNestsInsideAnHCTerminal(t *testing.T) {
	f := standardTerminals()
	f.env["HC_TERMINAL_ID"] = "t2"
	core := f.core()
	if err := core.Activate(context.Background(), "sp1"); err != nil {
		t.Fatal(err)
	}
	if !f.called("app do space.select --id sp1") {
		t.Fatalf("space activation must select it in Herdr Desktop; calls = %v", f.calls)
	}
	if err := core.Activate(context.Background(), "t4"); err != nil {
		t.Fatal(err)
	}
	if len(f.attached) != 0 {
		t.Fatalf("attached %v inside an hc terminal", f.attached)
	}
	delete(f.env, "HC_TERMINAL_ID")
	if err := core.Activate(context.Background(), "t4"); err != nil {
		t.Fatal(err)
	}
	if len(f.attached) != 1 || f.attached[0] != "herdr-core attach t4" {
		t.Fatalf("attached = %v", f.attached)
	}
}

func TestHerdrCoreCloseRequiresDevOwnership(t *testing.T) {
	f := standardTerminals()
	f.owned = []string{"t2"}
	if err := f.core().Close(context.Background(), "sp1"); err == nil || !strings.Contains(err.Error(), "t3") {
		t.Fatalf("Close of a space with a foreign terminal = %v, want refusal naming it", err)
	}
	if f.called("app do space.close") {
		t.Fatal("a space with a foreign terminal must not be closed")
	}
	if err := f.core().Close(context.Background(), "t4"); err == nil {
		t.Fatal("a terminal dev did not open must not be killed")
	}
	f.owned = []string{"t2", "t3", "t4"}
	if err := f.core().Close(context.Background(), "sp1"); err != nil {
		t.Fatal(err)
	}
	if !f.called("app do space.close --id sp1 --confirmed true") {
		t.Fatalf("calls = %v", f.calls)
	}
	if err := f.core().Close(context.Background(), "t4"); err != nil || !f.called("hc kill t4") {
		t.Fatalf("Close(t4) = %v; calls = %v", err, f.calls)
	}
}

func TestHerdrCoreAnnotateLabelsSpaceTerminals(t *testing.T) {
	f := standardTerminals()
	if err := f.core().Annotate(context.Background(), "sp1", map[string]string{"stage": "hot", "next": ""}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hc label t2 dev.next= dev.stage=hot --json", "hc label t3 dev.next= dev.stage=hot --json"} {
		if !f.called(want) {
			t.Fatalf("missing %q; calls = %v", want, f.calls)
		}
	}
}

func TestHerdrCoreCurrentPaneIDVerifiesInheritedAddress(t *testing.T) {
	f := standardTerminals()
	if _, err := f.core().CurrentPaneID(context.Background()); err == nil {
		t.Fatal("no HC_TERMINAL_ID must be an error")
	}
	f.env["HC_TERMINAL_ID"] = "t2"
	if id, err := f.core().CurrentPaneID(context.Background()); err != nil || id != "t2" {
		t.Fatalf("CurrentPaneID = %q, %v", id, err)
	}
	f.env["HC_TERMINAL_ID"] = "t99"
	if _, err := f.core().CurrentPaneID(context.Background()); err == nil {
		t.Fatal("a stale HC_TERMINAL_ID must not be trusted")
	}
}

func TestDetectHost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		spaces string
		kind   HostKind
		gui    string
	}{
		{name: "plain", env: map[string]string{}, kind: HostPlain},
		{name: "classic herdr", env: map[string]string{"HERDR_ENV": "1"}, kind: HostHerdr},
		{name: "herdr desktop", env: map[string]string{"HC_TERMINAL_ID": "t2"}, spaces: twoSpaces, kind: HostHerdrCore, gui: GUIHerdrDesktop},
		{name: "hc without window", env: map[string]string{"HC_TERMINAL_ID": "t2"}, kind: HostHerdrCore, gui: GUIUnknown},
		{name: "terminal not shown", env: map[string]string{"HC_TERMINAL_ID": "t77"}, spaces: twoSpaces, kind: HostHerdrCore, gui: GUIUnknown},
		{name: "tmux inside hc", env: map[string]string{"HC_TERMINAL_ID": "t2", "TMUX": "/tmp/tmux"}, spaces: twoSpaces, kind: HostTmux},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := standardTerminals()
			f.spaces, f.env = tc.spaces, tc.env
			got := detectHost(context.Background(), hostProbe{getenv: f.core().getenv, core: f.core(), stat: func(string) error { return errors.New("absent") }})
			if got.Kind != tc.kind || got.GUI != tc.gui {
				t.Fatalf("detectHost = %+v, want kind %s gui %q", got, tc.kind, tc.gui)
			}
		})
	}
}

func TestInsideHerdrCoreOnly(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if !insideHerdrCoreOnly(env(map[string]string{"HC_TERMINAL_ID": "t1"})) {
		t.Fatal("hc terminal must prefer herdr-core")
	}
	for _, inner := range []string{"HERDR_ENV", "TMUX", "ZELLIJ"} {
		if insideHerdrCoreOnly(env(map[string]string{"HC_TERMINAL_ID": "t1", inner: "1"})) {
			t.Fatalf("an inner %s must keep its own backend", inner)
		}
	}
}
