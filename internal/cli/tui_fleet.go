package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

// tuiFleetBackend keeps dashboard collection separate from the CLI's eager,
// authentication-capable fleet list. Neither descriptors nor background reads
// resolve a local runtime, discover local repositories, or ask for credentials.
type tuiFleetBackend struct {
	current func() *App
	run     func(context.Context, fleet.Host, []string, fleet.RunOptions) fleet.Result
	mu      sync.Mutex
	next    map[string]uint64
}

func newTUIFleetBackend(current func() *App) *tuiFleetBackend {
	return &tuiFleetBackend{current: current, next: make(map[string]uint64)}
}

func localFleetDescriptor() tui.FleetHostDescriptor {
	name := config.Hostname()
	return tui.FleetHostDescriptor{Key: "local:" + name, Name: name, Local: true, Target: "this machine", OS: goruntime.GOOS}
}

func fleetDescriptor(host fleet.Host) tui.FleetHostDescriptor {
	id := fleet.EndpointID(host)
	return tui.FleetHostDescriptor{Key: "remote:" + id, Name: host.Name, EndpointID: id, Target: host.Destination(), SSHAlias: host.SSHAlias, OS: host.EffectiveRemoteOS()}
}

func (b *tuiFleetBackend) LoadHosts(ctx context.Context) (tui.FleetHostsResult, error) {
	result := tui.FleetHostsResult{Hosts: []tui.FleetHostDescriptor{localFleetDescriptor()}, MaxParallel: fleet.DefaultConfig().Defaults.MaxParallel}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	cfg, err := loadFleetConfig(b.current())
	if err != nil {
		return result, err
	}
	result.MaxParallel = cfg.Defaults.MaxParallel
	for _, host := range cfg.Hosts {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Hosts = append(result.Hosts, fleetDescriptor(host))
	}
	return result, nil
}

func (b *tuiFleetBackend) LoadHostCache(ctx context.Context, descriptor tui.FleetHostDescriptor) (*fleet.HostResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if descriptor.Local {
		return nil, false, nil
	}
	host, err := b.host(descriptor)
	if err != nil {
		return nil, false, err
	}
	snapshot, at, ok := fleet.LoadCache(host)
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	cfg, err := loadFleetConfig(b.current())
	if err != nil {
		return nil, false, err
	}
	if _, err := b.host(descriptor); err != nil {
		return nil, false, err
	}
	fresh := cfg.Defaults.CacheTTL.Duration <= 0 || time.Since(at) <= cfg.Defaults.CacheTTL.Duration
	return &fleet.HostResult{Name: host.Name, State: fleet.HostStale, Snapshot: &snapshot, CachedAt: &at, FromCache: true, EndpointID: descriptor.EndpointID}, fresh, nil
}

func (b *tuiFleetBackend) host(descriptor tui.FleetHostDescriptor) (fleet.Host, error) {
	if descriptor.Local {
		return fleet.Host{}, errors.New("local fleet data comes from accepted repository observations")
	}
	cfg, err := loadFleetConfig(b.current())
	if err != nil {
		return fleet.Host{}, err
	}
	for _, host := range cfg.Hosts {
		if host.Name == descriptor.Name {
			expected := fleetDescriptor(host)
			if descriptor.Key != expected.Key || descriptor.EndpointID != expected.EndpointID {
				return fleet.Host{}, errors.New("fleet host connection changed; refresh the host list")
			}
			return host, nil
		}
	}
	return fleet.Host{}, errors.New("fleet host is no longer configured")
}

func (b *tuiFleetBackend) LoadHost(ctx context.Context, descriptor tui.FleetHostDescriptor) (fleet.HostResult, error) {
	return b.loadHost(ctx, descriptor, fleet.RetryNever)
}

func (b *tuiFleetBackend) loadHost(ctx context.Context, descriptor tui.FleetHostDescriptor, retry fleet.RetryPolicy) (fleet.HostResult, error) {
	if err := ctx.Err(); err != nil {
		return fleet.HostResult{}, err
	}
	host, err := b.host(descriptor)
	if err != nil {
		return fleet.HostResult{Name: descriptor.Name, EndpointID: descriptor.EndpointID, State: fleet.HostInvalid}, err
	}
	b.mu.Lock()
	b.next[host.Name]++
	generation := b.next[host.Name]
	b.mu.Unlock()
	timeout := host.CommandTimeout.Duration
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if retry == fleet.RetryNever && timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cached, cachedAt, haveCache := fleet.LoadCache(host)
	if err := ctx.Err(); err != nil {
		return fleet.HostResult{}, err
	}
	run := b.run
	if run == nil {
		run = func(ctx context.Context, host fleet.Host, args []string, options fleet.RunOptions) fleet.Result {
			return (fleet.Transport{Err: io.Discard}).RunWithOptions(ctx, host, args, nil, options)
		}
	}
	response := run(ctx, host, []string{"fleet", "_snapshot"}, fleet.RunOptions{Retry: retry})
	if errors.Is(ctx.Err(), context.Canceled) {
		return fleet.HostResult{}, ctx.Err()
	}
	if response.ExitCode == 0 && ctx.Err() == nil && response.CaptureError == "" {
		var snapshot fleet.Snapshot
		if json.Unmarshal(response.Stdout, &snapshot) != nil || !fleet.ValidateSnapshot(snapshot) {
			return cachedFleetFailure(host, fleet.HostInvalid, "remote dev returned invalid snapshot JSON", cached, cachedAt, haveCache), nil
		}
		// Serialize generation checks and cache publication. A superseded result
		// cannot overwrite a newer request, even if a test or transport ignores
		// cancellation. Endpoint identity is reloaded immediately before saving.
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.next[host.Name] != generation {
			return fleet.HostResult{}, context.Canceled
		}
		if _, err := b.host(descriptor); err != nil {
			return fleet.HostResult{}, err
		}
		if err := ctx.Err(); err != nil {
			return fleet.HostResult{}, err
		}
		if err := fleet.SaveCacheContext(ctx, host, snapshot); err != nil && ctx.Err() != nil {
			return fleet.HostResult{}, ctx.Err()
		}
		return fleet.HostResult{Name: host.Name, State: fleet.HostOK, Snapshot: &snapshot, PasswordAuth: response.UsedPassword, EndpointID: descriptor.EndpointID}, nil
	}
	state := fleet.HostIncompatible
	detail := strings.TrimSpace(string(response.Stderr))
	switch {
	case response.TimedOut || response.ExitCode == 124 || errors.Is(ctx.Err(), context.DeadlineExceeded):
		state = fleet.HostTimeout
	case response.CaptureError != "":
		state = fleet.HostInvalid
	case response.ExitCode == 127:
		state = fleet.HostNoDev
	case response.ExitCode == 255:
		state = fleet.HostUnreachable
	}
	if detail == "" {
		detail = fmt.Sprintf("remote command exited %d", response.ExitCode)
	}
	return cachedFleetFailure(host, state, detail, cached, cachedAt, haveCache), nil
}

func (b *tuiFleetBackend) RunAction(ctx context.Context, descriptor tui.FleetHostDescriptor, action string) (*exec.Cmd, error) {
	return tuiFleetHostActionProcess(ctx, b.current(), descriptor, action)
}
