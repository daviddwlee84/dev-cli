package cli

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/picker"
)

func TestSSHKeyPickerGeneratePlacesBareNameAndAsksComment(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	var out bytes.Buffer
	app := &App{Cfg: config.Default(), In: bufio.NewReader(strings.NewReader("work-key\n  lab laptop  \n")), Out: &out, Err: &out,
		remotesPath: f.remotesPath, sshHostRunner: f.runner, interactiveCheck: func() bool { return true },
		pickerSelect: func(_ context.Context, request picker.Request) (picker.Result, error) {
			for _, item := range request.Items {
				if item.Value == "generate" {
					return picker.Result{Item: item}, nil
				}
			}
			t.Fatalf("key picker lacks a generate choice: %+v", request.Items)
			return picker.Result{}, nil
		}}
	service, err := app.sshHosts()
	if err != nil {
		t.Fatal(err)
	}
	options, err := chooseSSHKeyInteractive(t.Context(), app, service, "lab", sshSetupOptions{}, filepath.Join(service.Paths().SSHDir, "id_ed25519_dev_lab"))
	want := filepath.Join(service.Paths().SSHDir, "work-key")
	if err != nil || !options.generateKey || options.keyPath != want || options.comment != "lab laptop" || options.keyPlan == nil || options.keyPlan.IdentityFile != want || options.keyPlan.Comment != "lab laptop" {
		t.Fatalf("options=%+v plan=%+v err=%v\n%s", options, options.keyPlan, err, out.String())
	}
	if !strings.Contains(out.String(), "Key comment") {
		t.Fatalf("comment prompt missing:\n%s", out.String())
	}
	for input, want := range map[string]string{"id_work": filepath.Join("/ssh", "id_work"), "~/x/id": "~/x/id", "sub/id": "sub/id", "$HOME/id": "$HOME/id", "": ""} {
		if got := normalizeSSHKeyPromptPath("/ssh", input); got != want {
			t.Fatalf("normalize(%q)=%q want %q", input, got, want)
		}
	}
}
