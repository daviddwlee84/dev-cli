package gitx_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestGitDiagnosticClassifiesAndRedacts(t *testing.T) {
	for _, tc := range []struct{ message, code string }{{"Authentication failed", "authentication"}, {"Permission denied (publickey)", "authentication"}, {"! main:main [rejected] (fetch first)", "remote-ahead"}, {"remote: GH013 repository rule violations found", "remote-policy"}, {"Could not resolve host: example.test", "connection"}, {"Your local changes would be overwritten", "local-blocker"}, {"unexpected error", "unknown"}} {
		d := gitx.Diagnose(&gitx.Error{Stdout: tc.message, Stderr: "https://person:MY_PASSWORD@example.test/repo?token=SECRET_QUERY\nAuthorization: Bearer TOP_SECRET\npassword=ANOTHER_SECRET\n\x1b[31mcolored\x1b[0m", Err: errors.New("exit status 1")})
		if d.Code != tc.code || d.Next == "" {
			t.Fatalf("%+v", d)
		}
		for _, secret := range []string{"MY_PASSWORD", "SECRET_QUERY", "TOP_SECRET", "ANOTHER_SECRET", "\x1b"} {
			if strings.Contains(d.Details, secret) {
				t.Fatalf("unsafe diagnostic contains %q", secret)
			}
		}
		if !strings.Contains(d.Details, tc.message) {
			t.Fatal("lost actionable evidence")
		}
	}
	if d := gitx.Diagnose(context.DeadlineExceeded); d.Code != "timeout" {
		t.Fatal(d)
	}
}
func TestGitDiagnosticBoundsOutputAndPrivateMaterial(t *testing.T) {
	text := "-----BEGIN PRIVATE KEY-----\nprivate fixture data\n-----END PRIVATE KEY-----\n" + strings.Repeat("long diagnostic ", 3000)
	d := gitx.Diagnose(&gitx.Error{Stderr: text, Err: errors.New("failed")})
	if !d.Truncated || len(d.Details) > 16*1024 || strings.Contains(d.Details, "private fixture data") {
		t.Fatalf("invalid bounded diagnostic: %d bytes", len(d.Details))
	}
}
func TestGitFailurePreservesPorcelainStdout(t *testing.T) {
	r := gittest.New(t)
	// An alias is a local deterministic command; no remote or credential is used.
	r.Git("config", "alias.diagnostic-failure", "!printf 'porcelain failure'; printf 'stderr failure' >&2; exit 7")
	_, err := gitx.RunUnattended(t.Context(), r.Root, "diagnostic-failure")
	var command *gitx.Error
	if !errors.As(err, &command) || !strings.Contains(command.Stdout, "porcelain failure") {
		t.Fatal("stdout lost")
	}
	d := gitx.Diagnose(err)
	if d.ExitCode != 7 || !strings.Contains(d.Details, "stderr failure") {
		t.Fatalf("%+v", d)
	}
	if _, err := os.Stat(filepath.Join(r.Root, "unexpected")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
