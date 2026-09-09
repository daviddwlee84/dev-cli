//go:build linux || darwin

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

func TestSSHFormatAndOrganizeCLIPlansAreStatic(t *testing.T) {
	f := newSSHCLIFixture(t)
	path := filepath.Join(f.home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := "Host box alt\n  HostName 192.0.2.1\n  ProxyCommand echo secret-sentinel\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"ssh", "format", "--json"}, {"ssh", "organize", "--group", "box=work", "--json"}} {
		out, _, err := f.run(args...)
		if err != nil {
			t.Fatal(err)
		}
		var doc sshFileDocument
		if err = json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("invalid JSON: %v %s", err, out)
		}
		if !strings.Contains(doc.Kind, "_plan") || doc.Status != "planned" {
			t.Fatal(doc)
		}
		if strings.Contains(doc.Kind, "organize") && len(doc.Blocks) == 0 {
			t.Fatal("organize JSON omits block selectors")
		}
		if strings.Contains(out, "secret-sentinel") {
			t.Fatal("sensitive preview leaked")
		}
		if f.runner.callCount() != 0 {
			t.Fatal("static edit plan invoked a provider")
		}
	}
	b, _ := os.ReadFile(path)
	if string(b) != raw {
		t.Fatal("planning changed config")
	}
	if _, err := os.Stat(filepath.Join(f.home, ".local", "share", "dev", "ssh-recovery")); !os.IsNotExist(err) {
		t.Fatal("planning created recovery state")
	}
}
func TestSSHFormattingCLIApplyRestoreAndConfirmation(t *testing.T) {
	f := newSSHCLIFixture(t)
	path := filepath.Join(f.home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := "Host box\n  HostName 192.0.2.1\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.run("ssh", "format", "--apply", "--json"); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	out, _, err := f.run("ssh", "format", "--apply", "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc sshFileDocument
	if err = json.Unmarshal([]byte(out), &doc); err != nil || doc.Result == nil {
		t.Fatal(out, err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "    HostName") {
		t.Fatal("wrong indentation")
	}
	_, _, err = f.run("ssh", "restore", doc.Result.Receipt, "--apply", "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != raw {
		t.Fatal("restore lost original bytes")
	}
}
func TestSSHManagementWizardKeepsSelectionsExplicit(t *testing.T) {
	var output strings.Builder
	app := &App{In: strings.NewReader("both\n"), Out: &output, Err: &output}
	app.pickerSelect = func(_ context.Context, r picker.Request) (picker.Result, error) {
		if r.Prompt == "Machine action" {
			return picker.Result{Item: picker.Item{Value: "remove"}}, nil
		}
		if !r.Multi {
			t.Fatal("remove must support multi-selection")
		}
		return picker.Result{Items: []picker.Item{{Value: "fleet:custom"}, {Value: "herdr:0123456789abcdef0123456789abcdef"}}}, nil
	}
	request, err := sshManagementWizard(context.Background(), app, sshflow.Inventory{Fleet: []sshflow.FleetHost{{Name: "custom", Alias: "box"}}})
	if err != nil || request.Action != "remove" || len(request.FleetHosts) != 1 || request.FleetHosts[0] != "custom" || len(request.HerdrProfiles) != 1 {
		t.Fatal(request, err)
	}
}
