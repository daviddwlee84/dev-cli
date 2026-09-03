package sshhost

import (
	"fmt"
	"sync/atomic"
)

// ServiceOptions carries scanner bounds and expansion seams. Zero values use
// conservative defaults.
type ServiceOptions struct {
	Discovery DiscoverOptions
}

// Service coordinates static discovery, semantic ssh -G queries, and local
// source-bound mutations.
type Service struct {
	paths   Paths
	runner  Runner
	options ServiceOptions
	id      uint64

	// Publication hooks are package-test seams for source swaps in the final
	// check-to-mutation and post-publication verification windows. Production
	// services leave them nil.
	beforeManagedCommit func()
	afterManagedCommit  func()
	beforeInitCommit    func()
	beforeKeyCommit     func()
}

var nextServiceID atomic.Uint64

// NewService validates paths and returns a service using runner. A nil runner
// is replaced by ExecRunner; discovery itself never consults it.
func NewService(paths Paths, runner Runner, options ...ServiceOptions) (*Service, error) {
	if err := paths.Validate(); err != nil {
		return nil, err
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	var resolved ServiceOptions
	if len(options) > 1 {
		return nil, fmt.Errorf("new SSH host service: expected at most one options value")
	}
	if len(options) == 1 {
		resolved = options[0]
	}
	resolved.Discovery = resolved.Discovery.withDefaults()
	return &Service{paths: paths, runner: runner, options: resolved, id: nextServiceID.Add(1)}, nil
}

// NewDefaultService derives ~/.ssh paths for the current user.
func NewDefaultService(runner Runner, options ...ServiceOptions) (*Service, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return nil, err
	}
	return NewService(paths, runner, options...)
}

// Paths returns the fixed filesystem surface used by the service.
func (s *Service) Paths() Paths { return s.paths }
