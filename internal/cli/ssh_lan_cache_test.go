package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
)

func TestSSHLANDiscoveryRescansEmptyFreshCacheButReusesCandidates(t *testing.T) {
	app, _, request := sshNativeTestApp(t)
	var dials atomic.Int32
	scope := sshdiscovery.InterfaceScope{Interface: "lan0", Index: 2, Address: "192.168.55.10", Prefix: "192.168.55.0/24"}
	app.sshDiscoveryService = sshdiscovery.NewService(nil, sshdiscovery.ServiceOptions{
		Interfaces: func() ([]sshdiscovery.InterfaceScope, error) { return []sshdiscovery.InterfaceScope{scope}, nil },
		LookupAddr: func(context.Context, string) ([]string, error) { return nil, errors.New("no PTR") },
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			client, server := net.Pipe()
			go func() { defer server.Close(); _, _ = io.WriteString(server, "SSH-2.0-OpenSSH\r\n") }()
			return client, nil
		},
	})
	lanScope, err := app.sshDiscovery().LANScope(request)
	if err != nil {
		t.Fatal(err)
	}
	empty := sshdiscovery.Report{Source: sshdiscovery.SourceLAN, Scope: lanScope, Status: sshdiscovery.StatusReady, Complete: true, ObservedAt: time.Now().UTC(), Candidates: []sshdiscovery.Candidate{}}
	if err := sshdiscovery.WriteCache(t.Context(), sshDiscoveryCacheDir(), empty); err != nil {
		t.Fatal(err)
	}
	report, err := runSSHDiscovery(t.Context(), app, sshdiscovery.SourceLAN, request, false, true)
	if err != nil || dials.Load() != 1 || len(report.Candidates) != 1 {
		t.Fatalf("empty fresh cache was reused: dials=%d report=%+v err=%v", dials.Load(), report, err)
	}
	report, err = runSSHDiscovery(t.Context(), app, sshdiscovery.SourceLAN, request, false, true)
	if err != nil || dials.Load() != 1 || len(report.Candidates) != 1 {
		t.Fatalf("fresh cached candidates were not reused: dials=%d report=%+v err=%v", dials.Load(), report, err)
	}
}
