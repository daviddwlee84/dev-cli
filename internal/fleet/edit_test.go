package fleet

import (
	"context"
	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrimaryHostPatchPreservesCommentsAndOtherRecords(t *testing.T) {
	raw := "# inventory\nschema_version = 1\n\n# keep first\n[[hosts]]\nname = 'first' # name comment\nssh_alias = 'one'\n[hosts.ssh_login_password_source]\ntype = 'bitwarden'\nitem = 'ssh-one'\n\n# keep second\n[[hosts]]\nname = \"second\"\nssh_alias = 'two'\n"
	renamed, err := patchPrimaryHost([]byte(raw), "first", "A \"quoted\" name", false)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(raw, "name = 'first'", `name = "A \"quoted\" name"`, 1)
	if string(renamed) != want {
		t.Fatalf("unexpected rename:\n%s", renamed)
	}
	removed, err := patchPrimaryHost([]byte(raw), "first", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(removed), "keep first") || strings.Contains(string(removed), "ssh-one") || !strings.Contains(string(removed), "# keep second\n[[hosts]]") {
		t.Fatalf("wrong removal boundary:\n%s", removed)
	}
	if _, err = patchPrimaryHost([]byte("hosts = [{name='inline',ssh_alias='box'}]"), "inline", "new", false); err == nil {
		t.Fatal("inline layout should need native edit")
	}
}
func TestPrimaryHostEditApplyAndRestore(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native writer")
	}
	ctx := context.Background()
	dir := t.TempDir()
	primary := filepath.Join(dir, "remotes.toml")
	raw := "schema_version = 1\n[[hosts]]\nname = 'nickname'\nssh_alias = 'box'\n"
	if err := os.WriteFile(primary, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := PlanHostEdit(ctx, primary, "nickname", "rename", "renamed")
	if err != nil {
		t.Fatal(err)
	}
	recovery := filepath.Join(dir, "recovery")
	r, err := ApplyHostEdit(ctx, p, recovery)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(primary)
	if err != nil || cfg.Hosts[0].Name != "renamed" {
		t.Fatal(cfg, err)
	}
	restore, err := configedit.RestorePlan(ctx, recovery, r.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = configedit.Apply(ctx, restore, recovery); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(primary)
	if string(b) != raw {
		t.Fatal("restore lost original TOML bytes")
	}
}
