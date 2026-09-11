package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/spf13/cobra"
)

type sshEntryRunnerFunc func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error)

func (f sshEntryRunnerFunc) Run(ctx context.Context, req sshhost.RunRequest) (sshhost.RunResult, error) {
	return f(ctx, req)
}

func TestSSHEntryInheritsExecutionContext(t *testing.T) {
	for _, choice := range []string{"manage", "organize"} {
		t.Run(choice, func(t *testing.T) {
			fixture := newSSHCLIFixture(t)
			var output bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			app := &App{In: strings.NewReader(""), Out: &output, Err: &output,
				interactiveCheck: func() bool { return true },
				pickerSelect: func(got context.Context, req picker.Request) (picker.Result, error) {
					if got != ctx || req.Prompt != "SSH" {
						t.Fatal("entry picker lost execution context")
					}
					return picker.Result{Item: picker.Item{Value: choice}}, nil
				},
			}
			root := newRootCommand(app)
			child, _, err := root.Find([]string{"ssh", choice})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			child.RunE = func(cmd *cobra.Command, _ []string) error {
				called = true
				if cmd.Context() != ctx {
					t.Fatal("child lost parent execution context")
				}
				cancel()
				return cmd.Context().Err()
			}
			root.SetArgs([]string{"--config", fixture.configPath, "--no-runtime", "ssh"})
			if err := root.ExecuteContext(ctx); !errors.Is(err, context.Canceled) || !called {
				t.Fatalf("called=%v err=%v", called, err)
			}
		})
	}
}

func TestSSHEntryManageCarriesContextThroughInventory(t *testing.T) {
	for _, cancelDuringRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "inventory", true: "cancellation"}[cancelDuringRun], func(t *testing.T) {
			fixture := newSSHCLIFixture(t)
			type contextKey struct{}
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), contextKey{}, "entry"), time.Minute)
			defer cancel()
			var output bytes.Buffer
			calls := 0
			app := &App{In: strings.NewReader(""), Out: &output, Err: &output,
				interactiveCheck: func() bool { return true },
				sshHostRunner: sshEntryRunnerFunc(func(got context.Context, req sshhost.RunRequest) (sshhost.RunResult, error) {
					calls++
					if got == nil || got.Value(contextKey{}) != "entry" || req.Name != "herdr" {
						t.Fatal("inventory lost parent context or invoked unexpected provider")
					}
					deadline, ok := got.Deadline()
					parentDeadline, _ := ctx.Deadline()
					if !ok || deadline.After(parentDeadline) {
						t.Fatal("inventory lost parent deadline")
					}
					if cancelDuringRun {
						cancel()
						if !errors.Is(got.Err(), context.Canceled) {
							t.Fatal("inventory lost cancellation")
						}
						return sshhost.RunResult{}, got.Err()
					}
					return sshhost.RunResult{Stdout: []byte("[]")}, nil
				}),
				pickerSelect: func(got context.Context, req picker.Request) (picker.Result, error) {
					if got != ctx {
						t.Fatal("wizard lost parent context")
					}
					if req.Prompt == "SSH" {
						return picker.Result{Item: picker.Item{Value: "manage"}}, nil
					}
					return picker.Result{}, errPromptCanceled
				},
			}
			root := newRootCommand(app)
			root.SetArgs([]string{"--config", fixture.configPath, "--remotes", fixture.remotesPath, "--no-runtime", "ssh"})
			if err := root.ExecuteContext(ctx); err == nil || calls != 1 {
				t.Fatalf("runner calls=%d err=%v", calls, err)
			}
		})
	}
}
