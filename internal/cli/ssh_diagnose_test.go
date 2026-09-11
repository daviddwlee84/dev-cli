package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type sshDiagnosisRunnerFunc func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error)

func (f sshDiagnosisRunnerFunc) Run(ctx context.Context, r sshhost.RunRequest) (sshhost.RunResult, error) {
	return f(ctx, r)
}
func TestSSHDiagnoseCLIJSONAndLiteralTarget(t *testing.T) {
	f := newSSHCLIFixture(t)
	var out, errOut bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &errOut, interactiveCheck: func() bool { return false }, sshHostRunner: sshDiagnosisRunnerFunc(func(ctx context.Context, r sshhost.RunRequest) (sshhost.RunResult, error) {
		if r.Args[0] == "-G" {
			return sshhost.RunResult{Stdout: []byte("hostname 192.0.2.30\nport 22\nuser example\nproxyjump jump\n")}, nil
		}
		return sshhost.RunResult{ExitCode: 255, Stderr: []byte("Permission denied (publickey). secret-sentinel")}, nil
	})}
	root := newRootCommand(app)
	root.SetArgs([]string{"--config", f.configPath, "--no-runtime", "ssh", "diagnose", "192.0.2.30", "--json"})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("failed auth returned success")
	}
	var result sshhost.Diagnosis
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Kind != "ssh_diagnosis" || result.Status != "not_ready" {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String()+errOut.String(), "secret-sentinel") {
		t.Fatal("raw error leaked")
	}
	if len(result.Attempts) != 1 || result.Attempts[0].Code != "proxy_path_failed" {
		t.Fatal(result)
	}
}
func TestSSHDiagnoseInvalidTargetHasNoJSON(t *testing.T) {
	f := newSSHCLIFixture(t)
	out, _, err := f.run("ssh", "diagnose", "host\ninvalid", "--json")
	if err == nil || out != "" || f.runner.callCount() != 0 {
		t.Fatalf("%q %v", out, err)
	}
}
