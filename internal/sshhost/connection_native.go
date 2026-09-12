package sshhost

import (
	"context"
	"reflect"
)

type nativeConnectionSnapshot struct {
	sources   *connectionSourceSnapshot
	effective EffectiveConfig
}

// Native-only ProxyCommand sessions retain the user's complete OpenSSH policy.
// They carry source/effective snapshots but no resolved-route or selected-key
// authority, and can never acquire a managed password broker through this API.
func (s *Service) prepareNativeProxyConnection(ctx context.Context, alias string, routeErr error, sources *connectionSourceSnapshot) (*PreparedConnection, error) {
	effective, err := s.Effective(ctx, alias)
	if sourceErr := s.checkConnectionSources(ctx, sources); sourceErr != nil {
		return nil, sourceErr
	}
	if err != nil {
		return nil, err
	}
	if !proxyCommandEnabled(effective) {
		return nil, routeErr
	}
	if jumps, err := parseProxyJump(effective.ProxyJump); err != nil || len(jumps) > 0 {
		return nil, routeErr
	}
	snapshot := &nativeConnectionSnapshot{sources: sources, effective: cloneEffective(effective)}
	route := Route{Alias: alias, TargetRemoteOS: RemoteOSUnknown, Diagnostics: []Diagnostic{{Code: "native_route_unresolved", Message: "Native ProxyCommand connection; managed route, selected-key and password-saving proofs are unavailable."}}}
	return &PreparedConnection{Alias: alias, Route: cloneRoute(route), NativeOnly: true, state: &preparedConnectionState{service: s, route: route, native: snapshot, sources: sources}}, nil
}

func (s *Service) revalidateNativeConnection(ctx context.Context, alias string, snapshot *nativeConnectionSnapshot) error {
	if err := s.checkConnectionSources(ctx, snapshot.sources); err != nil {
		return err
	}
	effective, err := s.Effective(ctx, alias)
	if sourceErr := s.checkConnectionSources(ctx, snapshot.sources); sourceErr != nil {
		return sourceErr
	}
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(effective, snapshot.effective) || !proxyCommandEnabled(effective) {
		return ErrSourceChanged
	}
	return nil
}
