//go:build windows

package fleet

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestWindowsAskpassUsesAdditionalInheritedHandle(t *testing.T) {
	const password = "windows fleet secret 4c71"
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "askpass-child")
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := runCommandWithAskpass(cmd, password); err != nil {
		t.Fatalf("run askpass child: %v; stderr=%s", err, stderr.Bytes())
	}
	if stdout.String() != password+"\n" {
		t.Fatalf("askpass output = %q", stdout.String())
	}
	if cmd.SysProcAttr == nil || len(cmd.SysProcAttr.AdditionalInheritedHandles) != 1 {
		t.Fatalf("additional inherited handles = %+v", cmd.SysProcAttr)
	}
	if len(cmd.ExtraFiles) != 0 {
		t.Fatalf("Windows askpass populated unsupported ExtraFiles: %v", cmd.ExtraFiles)
	}
	if strings.Contains(strings.Join(cmd.Args, "\x00"), password) || strings.Contains(strings.Join(cmd.Env, "\x00"), password) {
		t.Fatal("password leaked into Windows argv or environment")
	}
}
