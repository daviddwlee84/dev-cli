package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/picker"
)

func runSSHSetupPickerCLI(t *testing.T, f *sshCLIFixture, pick func(picker.Request) (picker.Result, error), args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &out, sshHostRunner: f.runner, interactiveCheck: func() bool { return true },
		pickerSelect: func(_ context.Context, request picker.Request) (picker.Result, error) { return pick(request) }}
	root := newRootCommand(app)
	root.SetContext(t.Context())
	root.SetArgs(append([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestSSHSetupWithoutKeyFlagsOffersKeyPickerOnlyInteractively(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	f.appendRootConfig("Host foreign\n    HostName foreign.example\n")
	if _, _, err := f.run("ssh", "setup", "foreign", "--target-os", "posix", "--yes", "--json"); err == nil || !strings.Contains(err.Error(), "outside an interactive terminal") {
		t.Fatalf("non-interactive setup without a key = %v", err)
	}
	var offered []picker.Item
	out, err := runSSHSetupPickerCLI(t, f, func(request picker.Request) (picker.Result, error) {
		if request.Prompt != "SSH key for foreign" {
			t.Fatalf("unexpected picker %+v", request)
		}
		offered = request.Items
		return picker.Result{}, picker.ErrCanceled
	}, "ssh", "setup", "foreign")
	if !errors.Is(err, errPromptCanceled) {
		t.Fatalf("err=%v out=%s", err, out)
	}
	generate := false
	for _, item := range offered {
		generate = generate || item.Value == "generate" && strings.HasSuffix(item.Description, "id_ed25519_dev_foreign")
	}
	if !generate || len(offered) == 0 || offered[len(offered)-1].Value != "manual" {
		t.Fatalf("key picker items=%+v", offered)
	}
	if _, err := os.Lstat(f.managedPath("foreign")); !os.IsNotExist(err) {
		t.Fatalf("canceled key picker created a fragment: %v", err)
	}
}

func TestSSHEntryKeySetupPicksHostThenKey(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	f.appendRootConfig("Host foreign\n    HostName foreign.example\n    User tester\n")
	var prompts []string
	_, err := runSSHSetupPickerCLI(t, f, func(request picker.Request) (picker.Result, error) {
		prompts = append(prompts, request.Prompt)
		switch request.Prompt {
		case "SSH":
			return picker.Result{Item: picker.Item{Value: "key"}}, nil
		case "SSH host for key setup":
			for _, item := range request.Items {
				if item.Value == "foreign" && strings.Contains(item.Description, "tester@foreign.example") {
					return picker.Result{Item: item}, nil
				}
			}
			t.Fatalf("host picker lacks the foreign alias: %+v", request.Items)
		}
		return picker.Result{}, picker.ErrCanceled
	}, "ssh")
	if !errors.Is(err, errPromptCanceled) || strings.Join(prompts, "|") != "SSH|SSH host for key setup|SSH key for foreign" {
		t.Fatalf("prompts=%v err=%v", prompts, err)
	}
}
