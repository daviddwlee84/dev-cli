package cli

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/tuiissue"
)

func TestTUIInstallReviewRevalidationAndNativeFailure(t *testing.T) {
	for _, scenario := range []string{"success", "canceled", "already-installed", "verification-failed"} {
		t.Run(scenario, func(t *testing.T) {
			installed := false
			runs, checks := 0, 0
			env := tuiissue.Environment{OS: "linux", Distribution: "debian", LookPath: func(tool string) (string, error) {
				if tool == "sudo" || tool == "apt-get" || installed {
					return "/reviewed/" + tool, nil
				}
				return "", exec.ErrNotFound
			}}
			hooks := tuiInstallHooks{
				Run: func(_ context.Context, path string, args []string, _ io.Reader, _, _ io.Writer) error {
					runs++
					if path != "/reviewed/sudo" || !slices.Equal(args, []string{"/reviewed/apt-get", "install", "openssh-client"}) {
						t.Fatalf("%s %v", path, args)
					}
					if scenario == "canceled" {
						return context.Canceled
					}
					installed = true
					return nil
				},
				Verify: func(_ context.Context, _ string, args []string) error {
					checks++
					if scenario == "verification-failed" {
						return errors.New("capability unavailable")
					}
					if !slices.Equal(args, []string{"-V"}) && !slices.Equal(args, []string{"-Q", "cipher"}) {
						t.Fatal(args)
					}
					return nil
				},
			}
			execution, err := prepareTUIInstallWithHooks(t.Context(), "ssh", env, hooks)
			if err != nil || execution.Command == nil || runs != 0 || checks != 0 || !strings.Contains(execution.Preview, "/reviewed/apt-get") {
				t.Fatalf("%+v %v", execution, err)
			}
			if scenario == "already-installed" {
				installed = true
			}
			err = execution.Command.Run()
			status, err := execution.Complete(err)
			switch scenario {
			case "success":
				if err != nil || runs != 1 || checks != 2 || !strings.Contains(status, "login remain separate") {
					t.Fatal(status, err, runs, checks)
				}
			case "already-installed":
				if err == nil || runs != 0 || checks != 0 {
					t.Fatal("stale prerequisites reached runner", err, runs, checks)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) || runs != 1 || checks != 0 {
					t.Fatal("cancel retried or verified", err, runs, checks)
				}
			case "verification-failed":
				if err == nil || runs != 1 || checks != 1 {
					t.Fatal("verification failure hidden", err, runs, checks)
				}
			}
		})
	}
}
func TestTUIUnsupportedInstallOnlyProvidesGuidance(t *testing.T) {
	env := tuiissue.Environment{OS: "windows", LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	execution, err := prepareTUIInstallWithHooks(t.Context(), "herdr", env, tuiInstallHooks{})
	if err != nil || execution.Command != nil || !strings.Contains(execution.Preview, "official") {
		t.Fatal(execution, err)
	}
}
