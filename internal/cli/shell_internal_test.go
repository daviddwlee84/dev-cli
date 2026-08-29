package cli

import (
	"bytes"
	"testing"
)

func TestCDDirectiveReportsDescriptorFailure(t *testing.T) {
	t.Setenv("DEV_SHELL_CD_FILE", "")
	t.Setenv("DEV_SHELL_CD_FD", "999999")
	app := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := app.cdDirective("/tmp/demo"); err == nil {
		t.Fatal("an invalid descriptor should fail the navigation command")
	}
}

func TestCDDirectiveFallsBackToPrintableCommand(t *testing.T) {
	t.Setenv("DEV_SHELL_CD_FILE", "")
	t.Setenv("DEV_SHELL_CD_FD", "")
	var out bytes.Buffer
	app := &App{Out: &out, Err: &bytes.Buffer{}}
	if err := app.cdDirective("/tmp/a directory"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "cd '/tmp/a directory'\n" {
		t.Errorf("fallback directive = %q", out.String())
	}
}
