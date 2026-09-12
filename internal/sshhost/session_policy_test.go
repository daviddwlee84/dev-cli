package sshhost

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionPolicyPreservesNativeScalarParsing(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("native OpenSSH unavailable")
	}
	paths := fixturePaths(t)
	effective := testProofEffective("target")
	effective.Values["remotecommand"] = []string{"printf 'hello world'"}
	effective.Values["requesttty"] = []string{"force"}
	effective.Values["sessiontype"] = []string{"default"}
	effective.Values["sendenv"] = []string{"LANG LC_*"}
	content, err := appendSessionPolicy([]byte("Host target\n HostName target.example\n IdentityFile none\n"), effective, ConnectionOptions{Interactive: true, ForwardAgentNo: true})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(paths.SSHDir, "session.conf")
	writeFixture(t, path, string(content))
	run, err := (ExecRunner{}).Run(context.Background(), RunRequest{Name: "ssh", Args: []string{"-F", path, "-G", "target"}, Display: "native session config fixture"})
	if err != nil || run.ExitCode != 0 {
		t.Fatal(err, string(run.Stderr))
	}
	parsed, err := ParseEffective("target", run.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if firstEffectiveValue(parsed, "remotecommand") != "printf 'hello world'" || firstEffectiveValue(parsed, "requesttty") != "force" {
		t.Fatal("session policy changed native semantics")
	}
}

func TestSessionPolicyRejectsUnsupportedEffectsBeforePasswordEntry(t *testing.T) {
	for _, key := range []string{"localforward", "remoteforward", "dynamicforward", "localcommand", "setenv", "remotecommand"} {
		t.Run(key, func(t *testing.T) {
			effective := testProofEffective("target")
			effective.Values[key] = []string{"configured-value"}
			if key == "localcommand" {
				effective.Values["permitlocalcommand"] = []string{"yes"}
			}
			if key == "remotecommand" {
				effective.Values[key] = []string{"echo %n"}
			}
			_, err := appendSessionPolicy(nil, effective, ConnectionOptions{Interactive: true, ForwardAgentNo: true})
			if !errors.Is(err, ErrUnsupportedRoute) || !strings.Contains(err.Error(), "configured") {
				t.Fatalf("unsupported %s silently dropped: %v", key, err)
			}
		})
	}
	effective := testProofEffective("target")
	effective.Values["localforward"] = []string{"1234 endpoint:22"}
	effective.Values["localcommand"] = []string{"user-command"}
	effective.Values["permitlocalcommand"] = []string{"yes"}
	if _, err := appendSessionPolicy(nil, effective, ConnectionOptions{SuppressForwarding: true}); err != nil {
		t.Fatalf("fixed helper cannot suppress unrelated forwarding: %v", err)
	}
}

func TestReconstructedRouteRejectsCredentialPathAliasTokens(t *testing.T) {
	for _, key := range []string{"identityfile", "certificatefile", "identityagent", "securitykeyprovider", "pkcs11provider"} {
		t.Run(key, func(t *testing.T) {
			effective := testProofEffective("original-alias")
			if key == "identityfile" {
				effective.IdentityFiles = []string{"~/.ssh/%n"}
			} else {
				effective.Values[key] = []string{"~/.ssh/%n"}
			}
			var body strings.Builder
			if err := writeOrdinaryProxyAuthentication(&body, effective); !errors.Is(err, ErrUnsupportedRoute) {
				t.Fatalf("alias token would silently change credential path: %v", err)
			}
		})
	}
}
