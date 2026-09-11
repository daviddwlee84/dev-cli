package sshdiscovery

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxLANAddresses = 256
	maxLANPorts     = 16
	maxLANEndpoints = 4096
	maxLANWorkers   = 32
	lanTimeout      = 30 * time.Second
	endpointTimeout = time.Second
	ptrTimeout      = 500 * time.Millisecond
	maxBannerBytes  = 4096
)

func listInterfaces() ([]InterfaceScope, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var scopes []InterfaceScope
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagPointToPoint != 0 || iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("read interface addresses: %w", err)
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil || !usableIPv4(prefix.Addr()) {
				continue
			}
			scopes = append(scopes, InterfaceScope{Interface: iface.Name, Index: iface.Index,
				Address: prefix.Addr().String(), Prefix: prefix.Masked().String()})
		}
	}
	return scopes, nil
}

// Interfaces reads local interface metadata; it sends no network packets.
func (s *Service) Interfaces() ([]InterfaceScope, error) {
	scopes, err := s.options.Interfaces()
	if err != nil {
		return nil, err
	}
	result := make([]InterfaceScope, 0, len(scopes))
	for _, scope := range scopes {
		address, addrErr := netip.ParseAddr(scope.Address)
		prefix, prefixErr := netip.ParsePrefix(scope.Prefix)
		if scope.Interface == "" || !safeText(scope.Interface, 128) || strings.ContainsAny(scope.Interface, ";#=,") || scope.Index <= 0 || addrErr != nil || prefixErr != nil ||
			!usableIPv4(address) || !prefix.Addr().Is4() || prefix.Bits() < 1 || !prefix.Contains(address) {
			return nil, ErrInvalidScope
		}
		scope.Prefix = prefix.Masked().String()
		result = append(result, scope)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Interface != result[j].Interface {
			return result[i].Interface < result[j].Interface
		}
		if result[i].Prefix != result[j].Prefix {
			return result[i].Prefix < result[j].Prefix
		}
		return result[i].Address < result[j].Address
	})
	return result, nil
}

type lanPlan struct {
	scope     string
	addresses []netip.Addr
	ports     []int
}

// LANScope validates and normalizes an explicit scan scope against current
// interfaces, without probing endpoints. It is also the exact cache scope key.
func (s *Service) LANScope(request LANRequest) (string, error) {
	plan, err := s.prepareLAN(request)
	return plan.scope, err
}

// CurrentLANScope checks whether a recorded scope still describes the same
// current interface, address set and explicitly selected ranges/ports. It reads
// local interface metadata only and does not probe or authenticate any endpoint.
func (s *Service) CurrentLANScope(scope string) (bool, error) {
	parts := strings.Split(scope, ";")
	if len(parts) != 4 || !strings.HasPrefix(parts[0], "interface=") || !strings.HasPrefix(parts[1], "onlink=") ||
		!strings.HasPrefix(parts[2], "ranges=") || !strings.HasPrefix(parts[3], "ports=") {
		return false, ErrInvalidScope
	}
	iface, index, ok := strings.Cut(strings.TrimPrefix(parts[0], "interface="), "#")
	if !ok || iface == "" || index == "" {
		return false, ErrInvalidScope
	}
	request := LANRequest{Interface: iface, Ranges: strings.Split(strings.TrimPrefix(parts[2], "ranges="), ",")}
	for _, text := range strings.Split(strings.TrimPrefix(parts[3], "ports="), ",") {
		port, err := strconv.Atoi(text)
		if err != nil {
			return false, ErrInvalidScope
		}
		request.Ports = append(request.Ports, port)
	}
	current, err := s.LANScope(request)
	if err != nil {
		return false, err
	}
	return current == scope, nil
}

func (s *Service) prepareLAN(request LANRequest) (lanPlan, error) {
	if len(request.Ranges) == 0 || len(request.Ranges) > maxLANAddresses {
		return lanPlan{}, fmt.Errorf("select explicit IPv4 ranges totaling at most %d addresses: %w", maxLANAddresses, ErrInvalidScope)
	}
	ports := append([]int(nil), request.Ports...)
	if len(ports) == 0 {
		ports = []int{22}
	}
	if len(ports) > maxLANPorts {
		return lanPlan{}, fmt.Errorf("at most %d ports are allowed: %w", maxLANPorts, ErrInvalidScope)
	}
	sort.Ints(ports)
	uniquePorts := ports[:0]
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return lanPlan{}, fmt.Errorf("invalid TCP port: %w", ErrInvalidScope)
		}
		if len(uniquePorts) == 0 || uniquePorts[len(uniquePorts)-1] != port {
			uniquePorts = append(uniquePorts, port)
		}
	}
	ports = uniquePorts
	addresses := map[netip.Addr]bool{}
	ranges := map[string]bool{}
	for _, raw := range request.Ranges {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			address, addrErr := netip.ParseAddr(raw)
			if addrErr != nil || !address.Is4() {
				return lanPlan{}, fmt.Errorf("LAN ranges must be literal IPv4 addresses or CIDRs: %w", ErrInvalidScope)
			}
			prefix = netip.PrefixFrom(address, 32)
		}
		if !prefix.Addr().Is4() || prefix.Bits() < 24 {
			return lanPlan{}, fmt.Errorf("each LAN range must contain at most %d addresses: %w", maxLANAddresses, ErrInvalidScope)
		}
		prefix = prefix.Masked()
		ranges[prefix.String()] = true
		for address := prefix.Addr(); address.IsValid() && prefix.Contains(address); address = address.Next() {
			if !usableIPv4(address) {
				return lanPlan{}, fmt.Errorf("range includes non-unicast IPv4 addresses: %w", ErrInvalidScope)
			}
			addresses[address] = true
			if len(addresses) > maxLANAddresses {
				return lanPlan{}, fmt.Errorf("LAN ranges exceed %d addresses: %w", maxLANAddresses, ErrInvalidScope)
			}
		}
	}
	if len(addresses)*len(ports) > maxLANEndpoints {
		return lanPlan{}, fmt.Errorf("LAN scope exceeds %d endpoints: %w", maxLANEndpoints, ErrInvalidScope)
	}
	scopes, err := s.Interfaces()
	if err != nil {
		return lanPlan{}, err
	}
	byInterface := map[string][]InterfaceScope{}
	for _, scope := range scopes {
		if request.Interface == "" || scope.Interface == request.Interface {
			key := scope.Interface + "#" + strconv.Itoa(scope.Index)
			byInterface[key] = append(byInterface[key], scope)
		}
	}
	var matching []string
	for key, group := range byInterface {
		containsAll := true
		for address := range addresses {
			contained := false
			for _, scope := range group {
				prefix, _ := netip.ParsePrefix(scope.Prefix)
				contained = contained || prefix.Contains(address)
			}
			containsAll = containsAll && contained
		}
		if containsAll {
			matching = append(matching, key)
		}
	}
	if len(matching) != 1 {
		return lanPlan{}, fmt.Errorf("ranges must belong to exactly one selected active broadcast interface: %w", ErrInvalidScope)
	}
	group := byInterface[matching[0]]
	self := map[netip.Addr]bool{}
	// Exclude every local address, including another interface on the same link.
	for _, scope := range scopes {
		address, _ := netip.ParseAddr(scope.Address)
		self[address] = true
	}
	var targets []netip.Addr
	for address := range addresses {
		if self[address] {
			continue
		}
		reserved := false
		for _, scope := range group {
			prefix, _ := netip.ParsePrefix(scope.Prefix)
			if prefix.Contains(address) && prefix.Bits() <= 30 {
				reserved = reserved || address == prefix.Addr() || address == lastIPv4(prefix)
			}
		}
		if !reserved {
			targets = append(targets, address)
		}
	}
	if len(targets) == 0 {
		return lanPlan{}, fmt.Errorf("scope contains no remote unicast addresses: %w", ErrInvalidScope)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Less(targets[j]) })
	var rangeNames, interfaceNames, portNames []string
	for value := range ranges {
		rangeNames = append(rangeNames, value)
	}
	for _, scope := range group {
		interfaceNames = append(interfaceNames, scope.Address+"/"+scope.Prefix)
	}
	for _, port := range ports {
		portNames = append(portNames, strconv.Itoa(port))
	}
	sort.Strings(rangeNames)
	sort.Strings(interfaceNames)
	scope := "interface=" + matching[0] + ";onlink=" + strings.Join(interfaceNames, ",") + ";ranges=" + strings.Join(rangeNames, ",") + ";ports=" + strings.Join(portNames, ",")
	return lanPlan{scope: scope, addresses: targets, ports: ports}, nil
}

func usableIPv4(address netip.Addr) bool {
	return address.Is4() && !address.IsUnspecified() && !address.IsLoopback() && !address.IsMulticast() && address != netip.MustParseAddr("255.255.255.255")
}

func lastIPv4(prefix netip.Prefix) netip.Addr {
	bytes := prefix.Masked().Addr().As4()
	bits := uint32(bytes[0])<<24 | uint32(bytes[1])<<16 | uint32(bytes[2])<<8 | uint32(bytes[3])
	bits |= uint32(0xffffffff) >> uint(prefix.Bits())
	return netip.AddrFrom4([4]byte{byte(bits >> 24), byte(bits >> 16), byte(bits >> 8), byte(bits)})
}

type lanEndpoint struct {
	address netip.Addr
	port    int
}
type lanResult struct{ candidate *Candidate }
type ptrResult struct {
	ready chan struct{}
	name  string
}

// LAN performs only bounded TCP connects and reads server identification. It
// never sends an SSH identification, authenticates, or executes a remote
// command. A completed scan means its probes ran, not that silent endpoints are
// closed or absent. Reverse-DNS names are suggestions and never connection truth.
func (s *Service) LAN(ctx context.Context, request LANRequest) (Report, error) {
	ctx, cancel := context.WithTimeout(nonNilContext(ctx), lanTimeout)
	defer cancel()
	report := s.report(SourceLAN, "")
	plan, err := s.prepareLAN(request)
	if err != nil {
		return report, err
	}
	report.Scope = plan.scope
	jobs := make(chan lanEndpoint)
	results := make(chan lanResult, maxLANWorkers)
	var workers sync.WaitGroup
	var ptr sync.Map
	for range min(maxLANWorkers, len(plan.addresses)*len(plan.ports)) {
		workers.Go(func() {
			for endpoint := range jobs {
				if ctx.Err() != nil {
					return
				}
				candidate := s.probeLANEndpoint(ctx, plan.scope, endpoint)
				if candidate != nil {
					candidate.DNSName = s.lookupPTR(ctx, endpoint.address.String(), &ptr)
					if candidate.DNSName != "" {
						candidate.Name, _, _ = strings.Cut(candidate.DNSName, ".")
					}
				}
				results <- lanResult{candidate: candidate}
			}
		})
	}
	go func() {
		defer close(jobs)
		for _, address := range plan.addresses {
			for _, port := range plan.ports {
				select {
				case jobs <- lanEndpoint{address, port}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	completed := 0
	for result := range results {
		completed++
		if result.candidate != nil {
			report.Candidates = append(report.Candidates, *result.candidate)
		}
	}
	sortCandidates(report.Candidates)
	report.Complete = ctx.Err() == nil && completed == len(plan.addresses)*len(plan.ports)
	report.Status = StatusReady
	if !report.Complete {
		report.Status = StatusPartial
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		return report, errors.New("LAN discovery did not finish every endpoint")
	}
	return report, nil
}

func (s *Service) probeLANEndpoint(ctx context.Context, scope string, endpoint lanEndpoint) *Candidate {
	ctx, cancel := context.WithTimeout(ctx, endpointTimeout)
	defer cancel()
	address := net.JoinHostPort(endpoint.address.String(), strconv.Itoa(endpoint.port))
	conn, err := s.options.DialContext(ctx, "tcp4", address)
	if err != nil {
		return nil
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetReadDeadline(deadline)
	candidate := &Candidate{Source: SourceLAN, Scope: scope, NativeID: address,
		Name: endpoint.address.String(), Addresses: []string{endpoint.address.String()}, Port: endpoint.port, State: StateOpen}
	candidate.ID = candidateID(SourceLAN, scope, address)
	reader := bufio.NewReaderSize(io.LimitReader(conn, maxBannerBytes), 512)
	for range 50 {
		line, err := reader.ReadString('\n')
		if err != nil {
			return candidate
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if len(line) > 253 || !safeText(line, 253) {
			return candidate
		}
		if strings.HasPrefix(line, "SSH-") {
			software := strings.TrimPrefix(line, "SSH-2.0-")
			if software == line {
				software = strings.TrimPrefix(line, "SSH-1.99-")
			}
			if software == line || software == "" || software[0] == ' ' {
				return candidate
			}
			candidate.State = StateSSH
			candidate.AdvertisesSSH = strings.HasPrefix(strings.ToLower(software), "tailscale")
			return candidate
		}
	}
	return candidate
}

func (s *Service) lookupPTR(ctx context.Context, address string, cache *sync.Map) string {
	entry := &ptrResult{ready: make(chan struct{})}
	value, loaded := cache.LoadOrStore(address, entry)
	if loaded {
		other := value.(*ptrResult)
		select {
		case <-other.ready:
			return other.name
		case <-ctx.Done():
			return ""
		}
	}
	defer close(entry.ready)
	ctx, cancel := context.WithTimeout(ctx, ptrTimeout)
	defer cancel()
	names, err := s.options.LookupAddr(ctx, address)
	if err != nil {
		return ""
	}
	var valid []string
	for _, name := range names {
		if validDNS(name) {
			valid = append(valid, strings.ToLower(strings.TrimSuffix(name, ".")))
		}
	}
	sort.Strings(valid)
	if len(valid) != 0 {
		entry.name = valid[0]
	}
	return entry.name
}
