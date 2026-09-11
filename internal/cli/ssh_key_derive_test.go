package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHKeyDerivePlansWithoutSubprocessAndNeverOverwrites(t *testing.T) {
	f := newSSHCLIFixture(t)
	f.initSSH()
	path := filepath.Join(f.home, ".ssh", "derive-me")
	if err := os.WriteFile(path, []byte("PRIVATE-KEY-BYTES-MUST-NOT-LEAK\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := f.run("ssh", "key", "derive", path, "--json")
	if err != nil || f.runner.callCount() != 0 {
		t.Fatal(out, err)
	}
	var doc sshKeyDeriveDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Status != "planned" {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(path + ".pub"); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out, _, err = f.run("ssh", "key", "derive", path, "--apply", "--yes", "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Status != "created" {
		t.Fatal(out, err)
	}
	if strings.Contains(out, "PRIVATE-KEY-BYTES") || strings.Contains(out, strings.Fields(string(f.runner.publicLine))[1]) {
		t.Fatal("key material in result")
	}
	before, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	f.runner.resetCalls()
	out, _, err = f.run("ssh", "key", "derive", path, "--apply", "--yes", "--json")
	if err != nil || f.runner.callCount() != 0 {
		t.Fatal(out, err)
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Status != "exists" {
		t.Fatal(out, err)
	}
	after, err := os.ReadFile(path + ".pub")
	if err != nil || string(before) != string(after) {
		t.Fatal(err)
	}
}
