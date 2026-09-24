package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/testutil"
)

const skillExecutionFixture = `package main
import ("fmt"; "io"; "os"; "time")
func main() {
 switch os.Args[1] {
 case "fail": os.Exit(7)
 case "wait":
  if err := os.WriteFile(os.Args[2], []byte("ready"), 0600); err != nil { panic(err) }
  for { time.Sleep(time.Hour) }
 default:
  body, err := io.ReadAll(os.Stdin); if err != nil { panic(err) }
  fmt.Fprint(os.Stdout, "out:"+string(body))
  fmt.Fprint(os.Stderr, "err:"+string(body))
 }
}
`

func isolatedSkillLease(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testutil.SetHome(t, home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "cache"))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(cache, "dev-cli", "skill-mutation")
}

func assertSkillLeaseAvailable(t *testing.T, directory string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	lease, err := lockx.AcquireDir(ctx, directory, "skill execution test")
	if err != nil {
		t.Fatalf("provider lease was retained: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSkillExecQueuesWithoutAcquiringProviderLease(t *testing.T) {
	provider := testutil.GoCommand(t, t.TempDir(), "skill-provider", skillExecutionFixture)
	for _, action := range []string{"add", "update"} {
		t.Run(action, func(t *testing.T) {
			directory := isolatedSkillLease(t)
			calls := 0
			factory := func() (*agentskill.MutationCommand, error) {
				calls++
				return &agentskill.MutationCommand{Command: exec.Command(provider, "echo")}, nil
			}
			model := New(Actions{AddSkill: factory, UpdateSkill: func(agentskill.Skill) (*agentskill.MutationCommand, error) { return factory() }}, nil, nil)
			model.view = ViewSkills
			var queued tea.Cmd
			if action == "add" {
				_, queued = model.executeDashboardAction(listActionSkillAdd)
			} else {
				model.skillUpdateTarget = agentskill.Skill{Name: "selected", Checkout: "/selected/repo"}
				queued = model.submit(modeConfirmSkillUpdate, "")
			}
			if queued == nil || calls != 1 {
				t.Fatalf("queued=%v provider selections=%d", queued, calls)
			}
			if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("model transition acquired the provider lease: %v", err)
			}
			// Producing an Exec message still does not mean Bubble Tea ran it.
			// A canceled dashboard or failed terminal release can discard it.
			if message := queued(); message == nil {
				t.Fatal("missing Exec message")
			}
			if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("queued Exec message acquired the provider lease: %v", err)
			}
			assertSkillLeaseAvailable(t, directory)
		})
	}
}

func TestSkillExecReleasesLeaseOnFailureAndCancellation(t *testing.T) {
	provider := testutil.GoCommand(t, t.TempDir(), "skill-provider", skillExecutionFixture)
	for _, outcome := range []string{"success", "exit", "start-failure", "canceled", "empty"} {
		t.Run(outcome, func(t *testing.T) {
			directory := isolatedSkillLease(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			command := exec.CommandContext(ctx, provider, "echo")
			switch outcome {
			case "exit":
				command = exec.CommandContext(ctx, provider, "fail")
			case "start-failure":
				command = exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-provider"))
			case "canceled":
				cancel()
			case "empty":
				command = nil
			}
			var out, errOut bytes.Buffer
			runner := &skillExecutionCommand{mutation: &agentskill.MutationCommand{Command: command}}
			runner.SetStdin(strings.NewReader("input"))
			runner.SetStdout(&out)
			runner.SetStderr(&errOut)
			err := runner.Run()
			if (err == nil) != (outcome == "success") {
				t.Fatalf("outcome %s: %v", outcome, err)
			}
			if outcome == "success" && (out.String() != "out:input" || errOut.String() != "err:input") {
				t.Fatalf("terminal streams = %q / %q", &out, &errOut)
			}
			assertSkillLeaseAvailable(t, directory)
		})
	}
}

func TestSkillExecHoldsLeaseUntilRunningProviderIsCanceled(t *testing.T) {
	directory := isolatedSkillLease(t)
	provider := testutil.GoCommand(t, t.TempDir(), "skill-provider", skillExecutionFixture)
	ready := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runner := &skillExecutionCommand{mutation: &agentskill.MutationCommand{Command: exec.CommandContext(ctx, provider, "wait", ready)}}
	done := make(chan error, 1)
	go func() { done <- runner.Run() }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("provider exited before readiness: %v", err)
		case <-deadline.C:
			t.Fatal("provider did not become ready")
		case <-ticker.C:
		}
	}
	contenderCtx, stop := context.WithTimeout(t.Context(), 80*time.Millisecond)
	lease, err := lockx.AcquireDir(contenderCtx, directory, "provider contender")
	stop()
	if lease != nil {
		_ = lease.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("running provider did not hold its lease: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled provider reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled provider did not return")
	}
	assertSkillLeaseAvailable(t, directory)
}

func TestSkillExecPreservesExplicitProviderStreams(t *testing.T) {
	isolatedSkillLease(t)
	provider := testutil.GoCommand(t, t.TempDir(), "skill-provider", skillExecutionFixture)
	var out, errOut, unused bytes.Buffer
	command := exec.Command(provider, "echo")
	command.Stdin, command.Stdout, command.Stderr = strings.NewReader("provider"), &out, &errOut
	runner := &skillExecutionCommand{mutation: &agentskill.MutationCommand{Command: command}}
	runner.SetStdin(strings.NewReader("terminal"))
	runner.SetStdout(&unused)
	runner.SetStderr(&unused)
	if err := runner.Run(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "out:provider" || errOut.String() != "err:provider" || unused.Len() != 0 {
		t.Fatalf("provider streams replaced: out=%q err=%q terminal=%q", &out, &errOut, &unused)
	}
}

type skillExecTestModel struct {
	command tea.Cmd
	result  tea.Msg
}

func (m *skillExecTestModel) Init() tea.Cmd { return m.command }
func (m *skillExecTestModel) View() string  { return "" }
func (m *skillExecTestModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if complete, ok := message.(execFinishedMsg); ok {
		m.result = complete.message
		return m, tea.Quit
	}
	return m, nil
}

func TestSkillExecReportsProcessAndPreparationErrorsThroughAfterExec(t *testing.T) {
	provider := testutil.GoCommand(t, t.TempDir(), "skill-provider", skillExecutionFixture)
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "process", true: "preparation"}[empty], func(t *testing.T) {
			directory := isolatedSkillLease(t)
			var mutation *agentskill.MutationCommand
			if !empty {
				mutation = &agentskill.MutationCommand{Command: exec.Command(provider, "fail")}
			}
			model := &skillExecTestModel{command: runSkillMutation(mutation, func(err error) tea.Msg {
				return skillProcessMsg{action: "update", name: "selected", checkout: "/selected/repo", err: err}
			})}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
			if _, err := program.Run(); err != nil {
				t.Fatal(err)
			}
			message, ok := model.result.(skillProcessMsg)
			if !ok || message.err == nil || message.name != "selected" || message.checkout != "/selected/repo" {
				t.Fatalf("completion lost selected identity or error: %#v", model.result)
			}
			assertSkillLeaseAvailable(t, directory)
		})
	}
}
