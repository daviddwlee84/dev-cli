package sshhost

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
)

// ConnectionOptions controls one native SSH invocation. Args are remote command
// arguments, never local SSH options. An empty Args opens the native session.
type ConnectionOptions struct {
	Args               []string `json:"-"`
	Stdin              []byte   `json:"-"`
	Interactive        bool     `json:"interactive,omitempty"`
	CaptureStdout      bool     `json:"-"`
	SuppressForwarding bool     `json:"suppress_forwarding,omitempty"`
	ForwardAgentNo     bool     `json:"forward_agent_no,omitempty"`
}

// PreparedConnection retains in-process authority for exactly one connection.
// Prepare performs route/local metadata reads only. Run owns native credential
// prompts and never retries the remote command, including an exit status of 255.
type PreparedConnection struct {
	Alias      string `json:"alias"`
	Route      Route  `json:"route"`
	NativeOnly bool   `json:"native_only,omitempty"`
	state      *preparedConnectionState
}

type preparedConnectionState struct {
	mu       sync.Mutex
	service  *Service
	route    Route
	material *keyMaterialState
	used     bool
	closed   bool
	native   *nativeConnectionSnapshot
	sources  *connectionSourceSnapshot
}

func (s *Service) PrepareConnection(ctx context.Context, alias string, candidate *KeyCandidate) (*PreparedConnection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var material *keyMaterialState
	if candidate != nil {
		var err error
		material, err = s.validateKeyCandidate(*candidate)
		if err != nil {
			return nil, err
		}
		if err := s.revalidateSelectedKeySources(ctx, material); err != nil {
			return nil, err
		}
	}
	sources, err := s.captureConnectionSources(ctx)
	if err != nil {
		return nil, err
	}
	route, err := s.ResolveRoute(ctx, RouteRequest{Alias: alias})
	if sourceErr := s.checkConnectionSources(ctx, sources); sourceErr != nil {
		return nil, sourceErr
	}
	if err != nil {
		if candidate == nil && errors.Is(err, ErrUnsupportedRoute) {
			return s.prepareNativeProxyConnection(ctx, alias, err, sources)
		}
		return nil, err
	}
	return &PreparedConnection{Alias: alias, Route: cloneRoute(route), state: &preparedConnectionState{service: s, route: route, material: material, sources: sources}}, nil
}

func (connection *PreparedConnection) Close() error {
	if connection == nil || connection.state == nil {
		return nil
	}
	connection.state.mu.Lock()
	defer connection.state.mu.Unlock()
	connection.state.closed = true
	return nil
}

func (connection *PreparedConnection) Run(ctx context.Context, options ConnectionOptions) (RunResult, error) {
	if connection == nil || connection.state == nil {
		return RunResult{}, errors.New("connection was not prepared by this service")
	}
	state := connection.state
	state.mu.Lock()
	if state.used || state.closed || connection.Alias != state.route.Alias || !routesEqual(connection.Route, state.route) || connection.NativeOnly != (state.native != nil) {
		state.mu.Unlock()
		return RunResult{}, errors.New("prepared connection is consumed, closed, or modified")
	}
	state.used = true
	state.mu.Unlock()
	defer connection.Close()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	s := state.service
	if err := s.checkConnectionSources(ctx, state.sources); err != nil {
		return RunResult{}, err
	}
	var fresh Route
	if state.native != nil {
		if err := s.revalidateNativeConnection(ctx, connection.Alias, state.native); err != nil {
			return RunResult{}, err
		}
	} else {
		var err error
		fresh, err = s.ResolveRoute(ctx, state.route.state.request)
		if sourceErr := s.checkConnectionSources(ctx, state.sources); sourceErr != nil {
			return RunResult{}, sourceErr
		}
		if err != nil {
			return RunResult{}, err
		}
		if !sameConnectionRoute(state.route, fresh) {
			return RunResult{}, ErrSourceChanged
		}
	}
	args := appendFreshSSHOptions(nil, !options.Interactive)
	destination := connection.Alias
	if state.material != nil {
		verification, err := s.verifyKeyPair(ctx, state.material, options.Interactive)
		if err != nil {
			return RunResult{}, err
		}
		material := *state.material
		material.pairVerification = cloneKeyPairVerification(verification)
		selector, cleanup, err := s.prepareKeySelector(&material)
		if err != nil {
			return RunResult{}, err
		}
		defer cleanup()
		hop := fresh.state.hops[len(fresh.state.hops)-1]
		if selector.agent != nil {
			configured, enabled, err := s.resolveIdentityAgent(firstEffectiveValue(hop.effective, "identityagent"))
			if err != nil || !enabled || configured != "" && configured != selector.agent.socket {
				return RunResult{}, ErrAgentPolicyMismatch
			}
		}
		content, selectedDestination, err := renderSelectedConnectionConfig(hop.proofRoute, selector, options.Interactive)
		if err != nil {
			return RunResult{}, err
		}
		content, err = appendSessionPolicy(content, hop.effective, options)
		if err != nil {
			return RunResult{}, err
		}
		config, err := createStagedFile(s.paths.SSHDir, content, nil)
		if err != nil {
			return RunResult{}, err
		}
		defer config.discard()
		args = append([]string{"-F", filepath.Join(config.dir, config.name)}, args...)
		destination = selectedDestination
	}
	if options.SuppressForwarding {
		args = append(args, "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "PermitLocalCommand=no")
	}
	if options.ForwardAgentNo && !options.SuppressForwarding {
		args = append(args, "-o", "ForwardAgent=no")
	}
	args = append(args, destination)
	args = append(args, options.Args...)
	if err := s.checkConnectionSources(ctx, state.sources); err != nil {
		return RunResult{}, err
	}
	return s.runner.Run(ctx, RunRequest{Name: "ssh", Args: args, Stdin: options.Stdin, Interactive: options.Interactive, CaptureStdout: options.CaptureStdout, Display: "SSH prepared connection"})
}

func sameConnectionRoute(left, right Route) bool {
	if !routesEqual(left, right) || left.state == nil || right.state == nil || len(left.state.hops) != len(right.state.hops) {
		return false
	}
	for index := range left.state.hops {
		if !reflect.DeepEqual(left.state.hops[index].effective, right.state.hops[index].effective) {
			return false
		}
	}
	return true
}
