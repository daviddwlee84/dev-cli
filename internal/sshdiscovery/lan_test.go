package sshdiscovery

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testScopes() ([]InterfaceScope, error) {
	return []InterfaceScope{{Interface: "eth0", Index: 2, Address: "192.168.10.10", Prefix: "192.168.10.0/24"}}, nil
}
func noPTR(context.Context, string) ([]string, error) { return nil, errors.New("no PTR") }

type bannerConn struct {
	reader io.Reader
	closed atomic.Bool
}

func (c *bannerConn) Read(data []byte) (int, error) { return c.reader.Read(data) }
func (c *bannerConn) Write([]byte) (int, error) {
	panic("discovery must not write an SSH identification or authenticate")
}
func (c *bannerConn) Close() error                   { c.closed.Store(true); return nil }
func (*bannerConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*bannerConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*bannerConn) SetDeadline(time.Time) error      { return nil }
func (*bannerConn) SetReadDeadline(time.Time) error  { return nil }
func (*bannerConn) SetWriteDeadline(time.Time) error { return nil }

func TestLANReadsBannerOnlyAndKeepsPTRAdvisory(t *testing.T) {
	var mu sync.Mutex
	var endpoints []string
	var conns []*bannerConn
	var lookups atomic.Int32
	service := NewService(nil, ServiceOptions{
		Interfaces: testScopes,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp4" {
				t.Errorf("network=%s", network)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > endpointTimeout {
				t.Error("endpoint deadline missing")
			}
			banner := "Welcome\r\nSSH-2.0-OpenSSH_10.3\r\n"
			if strings.HasSuffix(address, ":80") {
				banner = "HTTP/1.1 200 OK\r\n"
			}
			conn := &bannerConn{reader: strings.NewReader(banner)}
			mu.Lock()
			endpoints = append(endpoints, address)
			conns = append(conns, conn)
			mu.Unlock()
			return conn, nil
		},
		LookupAddr: func(context.Context, string) ([]string, error) {
			lookups.Add(1)
			return []string{"bad\nHost *", "SERVER.local."}, nil
		},
	})
	report, err := service.LAN(context.Background(), LANRequest{Interface: "eth0", Ranges: []string{"192.168.10.20"}, Ports: []int{80, 22}})
	if err != nil || !report.Complete || len(report.Candidates) != 2 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if lookups.Load() != 1 {
		t.Fatalf("PTR queries=%d", lookups.Load())
	}
	for _, candidate := range report.Candidates {
		if candidate.Name != "server" || candidate.DNSName != "server.local" || candidate.Addresses[0] != "192.168.10.20" || candidate.Online != nil || candidate.AdvertisesSSH {
			t.Fatalf("candidate=%#v", candidate)
		}
		want := StateSSH
		if candidate.Port == 80 {
			want = StateOpen
		}
		if candidate.State != want {
			t.Fatalf("candidate=%#v", candidate)
		}
	}
	for _, conn := range conns {
		if !conn.closed.Load() {
			t.Fatal("connection retained after discovery")
		}
	}
	if _, err := ResolveCandidate(report.Candidates, "192.168.10.20"); !errors.Is(err, ErrAmbiguous) {
		t.Fatal("same address across ports selected implicitly")
	}
}

func TestLANScopeRejectsBroadOffLinkAndImplicitScansBeforeDial(t *testing.T) {
	service := NewService(nil, ServiceOptions{Interfaces: testScopes, LookupAddr: noPTR, DialContext: func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("invalid scope reached dialer")
		return nil, nil
	}})
	for _, request := range []LANRequest{
		{},
		{Ranges: []string{"192.168.10.0/23"}},
		{Ranges: []string{"192.168.11.1"}},
		{Ranges: []string{"192.168.10.20"}, Interface: "absent"},
		{Ranges: []string{"server.local"}},
		{Ranges: []string{"::1"}},
		{Ranges: []string{"192.168.10.10"}},
		{Ranges: []string{"192.168.10.0", "192.168.10.255"}},
		{Ranges: []string{"192.168.10.20"}, Ports: []int{0}},
		{Ranges: []string{"192.168.10.20"}, Ports: make([]int, 17)},
	} {
		if _, err := service.LAN(context.Background(), request); !errors.Is(err, ErrInvalidScope) {
			t.Fatalf("request=%#v err=%v", request, err)
		}
	}
	a, err := service.LANScope(LANRequest{Ranges: []string{"192.168.10.20", "192.168.10.21"}, Ports: []int{2222, 22, 22}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.LANScope(LANRequest{Ranges: []string{"192.168.10.21/32", "192.168.10.20/32"}, Ports: []int{22, 2222}})
	if err != nil || a != b {
		t.Fatalf("normalized scope differs: %q %q %v", a, b, err)
	}
}

func TestLANExcludesLocalNetworkAndBroadcastAddresses(t *testing.T) {
	var mu sync.Mutex
	var scanned []string
	service := NewService(nil, ServiceOptions{Interfaces: testScopes, LookupAddr: noPTR, DialContext: func(_ context.Context, _ string, address string) (net.Conn, error) {
		mu.Lock()
		scanned = append(scanned, address)
		mu.Unlock()
		return nil, errors.New("closed or unknown")
	}})
	report, err := service.LAN(context.Background(), LANRequest{Interface: "eth0", Ranges: []string{"192.168.10.0/24"}})
	if err != nil || !report.Complete || len(report.Candidates) != 0 || len(scanned) != 253 {
		t.Fatalf("scanned=%d report=%#v err=%v", len(scanned), report, err)
	}
	sort.Strings(scanned)
	for _, endpoint := range scanned {
		if endpoint == "192.168.10.0:22" || endpoint == "192.168.10.10:22" || endpoint == "192.168.10.255:22" {
			t.Fatal("probed local or broadcast address")
		}
	}
}

func TestLANBoundsConcurrencyAndReportsCancellation(t *testing.T) {
	var active, maximum atomic.Int32
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	service := NewService(nil, ServiceOptions{Interfaces: testScopes, LookupAddr: noPTR, DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		if current == 32 {
			once.Do(func() { close(reached) })
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, errors.New("not connected")
	}})
	done := make(chan error, 1)
	go func() {
		_, err := service.LAN(context.Background(), LANRequest{Ranges: []string{"192.168.10.0/26"}})
		done <- err
	}()
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("worker pool did not reach bounded concurrency")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != 32 {
		t.Fatalf("concurrency=%d", maximum.Load())
	}
	service = NewService(nil, ServiceOptions{Interfaces: testScopes, LookupAddr: noPTR, DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	report, err := service.LAN(ctx, LANRequest{Ranges: []string{"192.168.10.0/26"}})
	if !errors.Is(err, context.DeadlineExceeded) || report.Complete || report.Status != StatusPartial {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestLANRequiresUnambiguousCurrentInterface(t *testing.T) {
	scopes := func() ([]InterfaceScope, error) {
		a, _ := testScopes()
		return append(a, InterfaceScope{Interface: "other0", Index: 3, Address: "192.168.10.11", Prefix: "192.168.10.0/24"}), nil
	}
	service := NewService(nil, ServiceOptions{Interfaces: scopes})
	if _, err := service.LANScope(LANRequest{Ranges: []string{"192.168.10.20"}}); !errors.Is(err, ErrInvalidScope) {
		t.Fatal(err)
	}
	if _, err := service.LANScope(LANRequest{Interface: "eth0", Ranges: []string{"192.168.10.20"}}); err != nil {
		t.Fatal(err)
	}
}

func TestLANIdentificationIsBoundedAndAdvisory(t *testing.T) {
	for _, test := range []struct {
		banner, state string
		tailscale     bool
	}{
		{"SSH-2.0-Tailscale\r\n", StateSSH, true},
		{"SSH-1.99-OpenSSH\r\n", StateSSH, false},
		{"SSH-1.5-old\r\n", StateOpen, false},
		{"SSH-2.0-\r\n", StateOpen, false},
		{"SSH-2.0-" + strings.Repeat("x", 300) + "\r\n", StateOpen, false},
		{strings.Repeat("x", maxBannerBytes*2), StateOpen, false},
	} {
		service := NewService(nil, ServiceOptions{DialContext: func(context.Context, string, string) (net.Conn, error) {
			return &bannerConn{reader: strings.NewReader(test.banner)}, nil
		}})
		candidate := service.probeLANEndpoint(context.Background(), "test", lanEndpoint{address: netip.MustParseAddr("192.168.10.20"), port: 22})
		if candidate == nil || candidate.State != test.state || candidate.AdvertisesSSH != test.tailscale {
			t.Fatalf("banner=%q candidate=%#v", test.banner, candidate)
		}
	}
}

func TestCurrentLANScopeRechecksInterfaceIdentityWithoutProbing(t *testing.T) {
	scopes, _ := testScopes()
	service := NewService(nil, ServiceOptions{
		Interfaces: func() ([]InterfaceScope, error) { return scopes, nil },
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("scope check attempted network discovery")
			return nil, nil
		},
	})
	scope, err := service.LANScope(LANRequest{Interface: "eth0", Ranges: []string{"192.168.10.20"}, Ports: []int{22}})
	if err != nil {
		t.Fatal(err)
	}
	if current, err := service.CurrentLANScope(scope); err != nil || !current {
		t.Fatalf("current=%t err=%v", current, err)
	}
	scopes[0].Index++
	if current, err := service.CurrentLANScope(scope); err != nil || current {
		t.Fatalf("changed interface identity current=%t err=%v", current, err)
	}
	if current, err := service.CurrentLANScope("malformed"); err == nil || current {
		t.Fatalf("invalid scope current=%t err=%v", current, err)
	}
}
