package cli

import (
	"context"
	"sync"
	"sync/atomic"
)

// tuiAppState publishes complete App snapshots. Config reload builds a private
// copy and commits it only while its generation context is still current, so
// canceled callbacks can keep reading their old immutable snapshot safely.
type tuiAppState struct {
	current atomic.Pointer[App]
	reload  sync.Mutex
}

func newTUIAppState(app *App) *tuiAppState {
	state := &tuiAppState{}
	state.current.Store(app)
	return state
}

func (s *tuiAppState) Current() *App { return s.current.Load() }

func (s *tuiAppState) Prepare(ctx context.Context) (*App, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.reload.Lock()
	defer s.reload.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	next := *s.Current()
	if err := next.Load(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &next, nil
}

func (s *tuiAppState) Commit(next *App) {
	if next != nil {
		s.current.Store(next)
	}
}
