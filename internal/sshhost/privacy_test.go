package sshhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type privacyNoRunner struct{ t *testing.T }

func (r privacyNoRunner) Run(context.Context, RunRequest) (RunResult, error) {
	r.t.Fatal("privacy discovery executed a process")
	return RunResult{}, nil
}
func TestPrivacyLiteralsNeverExecuteOrReadKeys(t *testing.T) {
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if e := os.Mkdir(ssh, 0o700); e != nil {
		t.Fatal(e)
	}
	config := []byte("Host fixture\n HostName 192.0.2.10\n User example-user\n IdentityFile ~/.ssh/nonexistent-key\nMatch exec \"must-never-run\"\n HostName guarded.example.invalid\n")
	if e := os.WriteFile(filepath.Join(ssh, "config"), config, 0o600); e != nil {
		t.Fatal(e)
	}
	paths := Paths{Home: home, SSHDir: ssh, RootConfig: filepath.Join(ssh, "config"), ManagedDir: filepath.Join(ssh, "dev.d")}
	service, err := NewService(paths, privacyNoRunner{t})
	if err != nil {
		t.Fatal(err)
	}
	values, _, err := service.PrivacyLiterals(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, v := range values {
		found[v.Value] = true
	}
	for _, value := range []string{"fixture", "192.0.2.10", "example-user", "~/.ssh/nonexistent-key"} {
		if !found[value] {
			t.Fatalf("missing lexical kind for %q", value)
		}
	}
}
