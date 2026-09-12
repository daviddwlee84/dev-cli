package sshhost

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectionHintsUseLiteralScannedBytesAndProvenance(t *testing.T) {
	paths := fixturePaths(t)
	child := filepath.Join(paths.SSHDir, "hosts.conf")
	writeFixture(t, paths.RootConfig, "Include hosts.conf\nHost tail\n HostName tail.tailnet.ts.net.\n")
	writeFixture(t, child, "Host box alias\n HostName 100.64.0.2\n User ubuntu\n Port 2222\nHost ipv6\n HostName fd7a:115c:a1e0::1\nHost other-*\n HostName ignored.example\n")
	service := newFixtureService(t, paths, DiscoverOptions{})
	hints, err := service.ConnectionHints(context.Background())
	if err != nil || len(hints) != 4 {
		t.Fatalf("hints=%+v err=%v", hints, err)
	}
	byAlias := map[string]ConnectionHint{}
	for _, hint := range hints {
		byAlias[hint.Alias] = hint
		if hint.State != "known" || !strings.HasPrefix(hint.Fingerprint, "sha256:") {
			t.Fatalf("hint=%+v", hint)
		}
	}
	box := byAlias["box"]
	if box.HostName != "100.64.0.2" || box.User != "ubuntu" || box.Port != 2222 || box.Source != (Location{Path: child, Line: 1}) || len(box.Provenance) != 1 {
		t.Fatalf("box=%+v", box)
	}
	if byAlias["alias"].HostName != box.HostName || byAlias["ipv6"].HostName != "fd7a:115c:a1e0::1" || byAlias["tail"].User != "" || byAlias["tail"].Port != 0 {
		t.Fatalf("hints=%+v", hints)
	}
	before := box.Fingerprint
	writeFixture(t, child, "# changed snapshot\nHost box\n HostName 100.64.0.2\n")
	after, err := service.ConnectionHints(context.Background())
	if err != nil || len(after) == 0 || after[0].Fingerprint == before {
		t.Fatalf("snapshot change not observed: %+v %v", after, err)
	}
}

func TestConnectionHintsLeaveAmbiguousOrDynamicEndpointsUnknown(t *testing.T) {
	for _, test := range []struct{ name, config string }{
		{"missing hostname", "Host box\n User user\n"},
		{"wildcard block", "Host box other-*\n HostName 100.64.0.2\n"},
		{"wildcard overlay", "Host box\n HostName 100.64.0.2\nHost *\n User other\n"},
		{"global field", "User other\nHost box\n HostName 100.64.0.2\n"},
		{"duplicate block", "Host box\n HostName 100.64.0.2\nHost box\n HostName 100.64.0.3\n"},
		{"duplicate field", "Host box\n HostName 100.64.0.2\n HostName 100.64.0.3\n"},
		{"dynamic host", "Host box\n HostName %h.tailnet.ts.net\n"},
		{"dynamic user", "Host box\n HostName 100.64.0.2\n User ${USERNAME}\n"},
		{"invalid port", "Host box\n HostName 100.64.0.2\n Port 0\n"},
		{"command proxy", "Host box\n HostName 100.64.0.2\n ProxyCommand command --secret\n"},
		{"canonicalization", "CanonicalizeHostname yes\nHost box\n HostName 100.64.0.2\n"},
		{"dynamic match", "Host box\n HostName 100.64.0.2\nMatch exec \"never-run-secret\"\n User other\n"},
		{"incomplete include", "Include missing-${UNSET}/conf\nHost box\n HostName 100.64.0.2\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := fixturePaths(t)
			writeFixture(t, paths.RootConfig, test.config)
			service := newFixtureService(t, paths, DiscoverOptions{Environment: func(string) (string, bool) { return "", false }})
			hints, err := service.ConnectionHints(context.Background())
			if err != nil || len(hints) != 1 || hints[0].State != "unknown" || hints[0].HostName != "" || hints[0].User != "" || hints[0].Port != 0 {
				t.Fatalf("hints=%+v err=%v", hints, err)
			}
		})
	}
}

func TestConnectionHintsDoNotTreatIncludedInheritedFieldsAsSimpleBlock(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Host box\n Include connection.conf\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "connection.conf"), "HostName 100.64.0.2\n")
	hints, err := newFixtureService(t, paths, DiscoverOptions{}).ConnectionHints(context.Background())
	if err != nil || len(hints) != 1 || hints[0].State != "unknown" {
		t.Fatalf("hints=%+v err=%v", hints, err)
	}
}

func TestConnectionHintsPreserveBoundsAndCancellation(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Host box\n HostName "+strings.Repeat("x", 80)+"\n")
	service := newFixtureService(t, paths, DiscoverOptions{MaxLineBytes: 64})
	hints, err := service.ConnectionHints(context.Background())
	if err != nil || len(hints) != 1 || hints[0].State != "unknown" {
		t.Fatalf("hints=%+v err=%v", hints, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ConnectionHints(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestConnectionHintsNeverSerializeDynamicGuardCommands(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Match exec \"SECRET_COMMAND_MUST_NOT_LEAK\"\n Include guarded.conf\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "guarded.conf"), "Host box\n HostName 100.64.0.2\n")
	hints, err := newFixtureService(t, paths, DiscoverOptions{}).ConnectionHints(context.Background())
	if err != nil || len(hints) != 1 || hints[0].State != "unknown" {
		t.Fatalf("hints=%+v err=%v", hints, err)
	}
	data, err := json.Marshal(hints)
	if err != nil || strings.Contains(string(data), "SECRET_COMMAND") {
		t.Fatalf("unsafe hints: %s err=%v", data, err)
	}
}
