package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func TestSSHRegistrationDestinationsAcceptOptionalSets(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		want   string
	}{
		{name: "none"},
		{name: "fleet", values: []string{"fleet"}, want: "fleet"},
		{name: "herdr", values: []string{"herdr"}, want: "herdr"},
		{name: "both", values: []string{"fleet", "herdr"}, want: "both"},
		{name: "reversed", values: []string{"herdr", "fleet"}, want: "both"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := &App{Out: io.Discard}
			app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
				if request.Prompt != "Optional registration" || !request.Multi || len(request.Selected) != 0 {
					t.Fatalf("unexpected request: %+v", request)
				}
				var values []string
				for _, item := range request.Items {
					values = append(values, item.Value)
				}
				if !reflect.DeepEqual(values, []string{"fleet", "herdr"}) {
					t.Fatalf("destinations = %v", values)
				}
				result := picker.Result{Items: []picker.Item{}}
				for _, value := range test.values {
					result.Items = append(result.Items, picker.Item{Value: value})
				}
				return result, nil
			}
			got, err := sshRegistrationDestinations(t.Context(), app, "Optional registration")
			if err != nil || got != test.want {
				t.Fatalf("got=%q error=%v want=%q", got, err, test.want)
			}
		})
	}
}

func TestSSHRegistrationDestinationsCancellationAndInvalidResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		result picker.Result
		err    error
		want   error
	}{
		{name: "escape", err: picker.ErrCanceled, want: errPromptCanceled},
		{name: "context canceled", err: context.Canceled, want: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, want: context.DeadlineExceeded},
		{name: "unknown", result: picker.Result{Items: []picker.Item{{Value: "both"}}}},
		{name: "duplicate", result: picker.Result{Items: []picker.Item{{Value: "fleet"}, {Value: "fleet"}}}},
		{name: "single result", result: picker.Result{Item: picker.Item{Value: "fleet"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := &App{pickerSelect: func(context.Context, picker.Request) (picker.Result, error) { return test.result, test.err }}
			got, err := sshRegistrationDestinations(t.Context(), app, "Registration")
			if got != "" || err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("got=%q error=%v want=%v", got, err, test.want)
			}
		})
	}
	if _, err := sshRegistrationDestinations(t.Context(), &App{In: strings.NewReader(""), Out: io.Discard}, "Registration"); err == nil {
		t.Fatal("noninteractive selection was silently accepted as none")
	}
}

func TestSSHRegistrationDoesNotRelaxRequiredSelections(t *testing.T) {
	app := &App{pickerSelect: func(context.Context, picker.Request) (picker.Result, error) {
		return picker.Result{Items: []picker.Item{}}, nil
	}}
	if _, err := sshPick(t.Context(), app, "Hosts", []picker.Item{{Value: "host"}}, true); !errors.Is(err, errPromptCanceled) {
		t.Fatalf("required host selection error = %v", err)
	}
}

func TestSSHPickerCancellationIsRecognizedByWorkflows(t *testing.T) {
	for _, multi := range []bool{false, true} {
		app := &App{pickerSelect: func(context.Context, picker.Request) (picker.Result, error) {
			return picker.Result{}, picker.ErrCanceled
		}}
		if _, err := sshPick(t.Context(), app, "Hosts", []picker.Item{{Value: "host"}}, multi); !errors.Is(err, errPromptCanceled) {
			t.Fatalf("multi=%t cancellation=%v", multi, err)
		}
	}
	app := &App{pickerSelect: func(context.Context, picker.Request) (picker.Result, error) {
		return picker.Result{}, context.DeadlineExceeded
	}}
	if _, err := sshPick(t.Context(), app, "Hosts", []picker.Item{{Value: "host"}}, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline became user cancellation: %v", err)
	}
}

type sshRegistrationNoInput struct{ calls int }

func (r *sshRegistrationNoInput) Read([]byte) (int, error) {
	r.calls++
	return 0, errors.New("registration requested input after choosing no destinations")
}

func TestSSHTUIRegistrationPromptsOnlyForSelectedProviders(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		want   string
		fleet  bool
		herdr  bool
	}{
		{name: "none"},
		{name: "fleet", values: []string{"fleet"}, want: "fleet", fleet: true},
		{name: "herdr", values: []string{"herdr"}, want: "herdr", herdr: true},
		{name: "both", values: []string{"fleet", "herdr"}, want: "both", fleet: true, herdr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			input := &sshRegistrationNoInput{}
			app := &App{In: strings.NewReader("\n\n\n\n"), Out: &output, Err: &output}
			if len(test.values) == 0 {
				app.In = input
			}
			app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
				if !request.Multi {
					t.Fatal("registration was not multi-select")
				}
				result := picker.Result{}
				for _, value := range test.values {
					result.Items = append(result.Items, picker.Item{Value: value})
				}
				return result, nil
			}
			options, err := sshTUIRegistrationOptions(t.Context(), app, sshflow.MachineRow{}, "box")
			if err != nil || options.to != test.want || input.calls != 0 {
				t.Fatalf("options=%+v error=%v reads=%d", options, err, input.calls)
			}
			if strings.Contains(output.String(), "Fleet profile name") != test.fleet || strings.Contains(output.String(), "Herdr label") != test.herdr || strings.Contains(output.String(), "Herdr session") != test.herdr {
				t.Fatalf("provider prompts = %q", output.String())
			}
			if test.want == "" && (output.Len() != 0 || options.targetOS != "") {
				t.Fatalf("none choice solicited extra fields: %q %+v", output.String(), options)
			}
		})
	}
}

func TestSSHTUIRegistrationNoneDoesNotAuthenticateOrChangeMembership(t *testing.T) {
	app, fixture := sshTUITestApp(t)
	inv, err := loadSSHMachineInventory(t.Context(), app, false, true)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(fixture.rootConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	input := &sshRegistrationNoInput{}
	app.In = input
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		if request.Prompt != "Register this SSH profile with" || !request.Multi {
			t.Fatalf("unexpected prompt: %+v", request)
		}
		return picker.Result{Items: []picker.Item{}}, nil
	}
	fixture.runner.resetCalls()
	workflow, err := sshTUIActions(newTUIAppState(app)).Workflow(t.Context(), tui.SSHWorkflowRequest{Action: "register", Selected: inv.Machines[0]})
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Run(); err != nil {
		t.Fatal(err)
	}
	if input.calls != 0 || workflow.Result().MembershipChanged || workflow.Result().Status != "No registration selected." {
		t.Fatalf("reads=%d result=%+v", input.calls, workflow.Result())
	}
	for _, call := range fixture.runner.callSnapshot() {
		if call.Name != "herdr" || strings.Join(call.Args, " ") != "machine list --json" {
			t.Fatalf("empty registration performed %s %v", call.Name, call.Args)
		}
	}
	after, err := os.ReadFile(fixture.rootConfigPath())
	if err != nil || string(before) != string(after) {
		t.Fatal("empty registration changed SSH config")
	}
	if _, err := os.Stat(app.machineStore().Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty registration created machine registry: %v", err)
	}
}

func TestSSHManageRegistrationNoneSkipsPlanAndProviderPrompts(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	f.appendRootConfig("Host box\n HostName example.invalid\n")
	var output strings.Builder
	input := &sshRegistrationNoInput{}
	app := &App{Cfg: config.Default(), In: input, Out: &output, Err: &output, remotesPath: f.remotesPath, sshHostRunner: f.runner, interactiveCheck: func() bool { return true }}
	app.pickerSelect = func(_ context.Context, request picker.Request) (picker.Result, error) {
		switch request.Prompt {
		case "Machine action":
			return picker.Result{Item: picker.Item{Value: "register"}}, nil
		case "Select records":
			return picker.Result{Items: []picker.Item{{Value: "box"}}}, nil
		case "Register with":
			if !request.Multi {
				t.Fatal("registration was not multi-select")
			}
			return picker.Result{Items: []picker.Item{}}, nil
		default:
			t.Fatalf("unexpected prompt: %+v", request)
			return picker.Result{}, errPromptCanceled
		}
	}
	f.runner.resetCalls()
	cmd := newSSHManageCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(nil)
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if input.calls != 0 || !strings.Contains(output.String(), "No registration selected.") || strings.Contains(output.String(), "Apply these selected operations?") {
		t.Fatalf("reads=%d output=%s", input.calls, output.String())
	}
	for _, call := range f.runner.callSnapshot() {
		if call.Name != "herdr" || strings.Join(call.Args, " ") != "machine list --json" {
			t.Fatalf("empty registration performed %s %v", call.Name, call.Args)
		}
	}
}
