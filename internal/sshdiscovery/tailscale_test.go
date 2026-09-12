package sshdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type runnerFunc func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error)

func (fn runnerFunc) Run(ctx context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
	return fn(ctx, request)
}

func tailscaleFixture(peers map[string]any) []byte {
	data, _ := json.Marshal(map[string]any{
		"Version": "1.102.3", "BackendState": "Running", "Self": map[string]any{"ID": "self1"},
		"CurrentTailnet": map[string]any{"MagicDNSSuffix": "tail123.ts.net"}, "Peer": peers,
	})
	return data
}

func validPeer(id, name string) map[string]any {
	return map[string]any{"ID": id, "HostName": name, "DNSName": name + ".tail123.ts.net.", "OS": "linux", "TailscaleIPs": []string{"fd7a:115c:a1e0::1", "100.77.43.16"}, "Online": false}
}

func TestTailscaleMinimalVersionedDiscovery(t *testing.T) {
	peer := validPeer("node1", "ubuntu")
	peer["sshHostKeys"] = []string{"ssh-ed25519 public-material"}
	peer["LastSeen"] = "2026-09-10T01:02:03Z"
	peer["NewFutureField"] = map[string]string{"arbitrary": "ignored"}
	unknown := validPeer("node2", "mac")
	delete(unknown, "Online")
	self := validPeer("self1", "local")
	sharee := validPeer("node3", "sharee")
	sharee["ShareeNode"] = true
	data := tailscaleFixture(map[string]any{"nodekey:changing1": peer, "nodekey:changing2": unknown, "self": self, "sharee": sharee})
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	service := NewService(runnerFunc(func(ctx context.Context, request sshhost.RunRequest) (sshhost.RunResult, error) {
		if request.Name != "tailscale" || !reflect.DeepEqual(request.Args, []string{"status", "--json"}) || request.Interactive || len(request.Stdin) != 0 {
			t.Fatalf("unexpected request: %#v", request)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > tailscaleTimeout {
			t.Fatal("missing discovery deadline")
		}
		return sshhost.RunResult{Stdout: data}, nil
	}), ServiceOptions{Now: func() time.Time { return now }})
	report, err := service.Tailscale(context.Background())
	if err != nil || !report.Complete || report.Status != StatusReady || len(report.Candidates) != 2 || report.ObservedAt != now {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	ubuntu, err := ResolveCandidate(report.Candidates, "UBUNTU")
	if err != nil || ubuntu.NativeID != "node1" || ubuntu.Online == nil || *ubuntu.Online || !ubuntu.AdvertisesSSH || ubuntu.LastSeen == nil {
		t.Fatalf("candidate=%#v err=%v", ubuntu, err)
	}
	if !reflect.DeepEqual(ubuntu.Addresses, []string{"100.77.43.16", "fd7a:115c:a1e0::1"}) {
		t.Fatal(ubuntu.Addresses)
	}
	mac, _ := ResolveCandidate(report.Candidates, "mac.tail123.ts.net.")
	if mac.Online != nil || mac.AdvertisesSSH {
		t.Fatal("missing observations became false facts")
	}
	if strings.Contains(ubuntu.ID, "changing") {
		t.Fatal("candidate identity used rotating node key")
	}
}

func TestTailscaleFailureStatesRemainDistinctFromNoPeers(t *testing.T) {
	for _, test := range []struct {
		name       string
		data       []byte
		result     sshhost.RunResult
		err        error
		wantStatus string
		wantError  error
	}{
		{name: "optional missing", err: &exec.Error{Name: "tailscale", Err: exec.ErrNotFound}, wantStatus: StatusUnavailable, wantError: ErrUnavailable},
		{name: "daemon unavailable", result: sshhost.RunResult{ExitCode: 1, Stderr: []byte("private daemon information")}, wantStatus: StatusFailed},
		{name: "login needed", data: []byte(`{"BackendState":"NeedsLogin","AuthURL":"secret-url"}`), wantStatus: StatusUnavailable, wantError: ErrUnavailable},
		{name: "truncated", data: tailscaleFixture(nil), result: sshhost.RunResult{StdoutTruncated: true}, wantStatus: StatusFailed, wantError: ErrInvalidData},
		{name: "invalid JSON", data: []byte(`{`), wantStatus: StatusFailed, wantError: ErrInvalidData},
		{name: "missing peers", data: []byte(`{"BackendState":"Running","Self":{"ID":"self"}}`), wantStatus: StatusFailed, wantError: ErrInvalidData},
		{name: "missing local identity", data: []byte(`{"BackendState":"Running","Peer":{}}`), wantStatus: StatusFailed, wantError: ErrInvalidData},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(runnerFunc(func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error) {
				r := test.result
				r.Stdout = test.data
				return r, test.err
			}))
			report, err := service.Tailscale(context.Background())
			if err == nil || report.Status != test.wantStatus || report.Complete || len(report.Candidates) != 0 {
				t.Fatalf("report=%#v err=%v", report, err)
			}
			if test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "secret-url") || strings.Contains(err.Error(), "private daemon") {
				t.Fatal("raw process data escaped")
			}
		})
	}
	service := NewService(runnerFunc(func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error) {
		return sshhost.RunResult{Stdout: tailscaleFixture(map[string]any{})}, nil
	}))
	report, err := service.Tailscale(context.Background())
	if err != nil || !report.Complete || report.Status != StatusReady || len(report.Candidates) != 0 {
		t.Fatalf("empty valid tailnet: %#v %v", report, err)
	}
}

func TestTailscaleInvalidPeersMakePartialInventory(t *testing.T) {
	good := validPeer("good", "good")
	bad := validPeer("bad", "bad")
	bad["HostName"] = "bad\nHost *"
	duplicate := validPeer("duplicate", "duplicate")
	service := NewService(runnerFunc(func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error) {
		return sshhost.RunResult{Stdout: tailscaleFixture(map[string]any{"a": good, "b": bad, "c": duplicate, "d": duplicate})}, nil
	}))
	report, err := service.Tailscale(context.Background())
	if !errors.Is(err, ErrInvalidData) || report.Complete || report.Status != StatusPartial || len(report.Candidates) != 1 || report.Candidates[0].NativeID != "good" {
		t.Fatalf("%#v %v", report, err)
	}
}

func TestResolveCandidateRequiresUniqueExactSelection(t *testing.T) {
	a := Candidate{ID: "a", Name: "same", DNSName: "same.one.ts.net", Addresses: []string{"100.64.0.1"}, Port: 22}
	b := Candidate{ID: "b", Name: "same", DNSName: "same.two.ts.net", Addresses: []string{"100.64.0.1"}, Port: 2222}
	for _, selector := range []string{"same", "100.64.0.1"} {
		if _, err := ResolveCandidate([]Candidate{a, b}, selector); !errors.Is(err, ErrAmbiguous) {
			t.Fatalf("%s: %v", selector, err)
		}
	}
	for _, selector := range []string{"a", "SAME.ONE.TS.NET."} {
		result, err := ResolveCandidate([]Candidate{a, b}, selector)
		if err != nil || result.ID != "a" {
			t.Fatalf("%s: %#v %v", selector, result, err)
		}
	}
	if _, err := ResolveCandidate([]Candidate{a, b}, "sam"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
