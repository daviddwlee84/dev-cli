package gitx

import (
	"context"
	"path/filepath"
	"sync"
)

type observationKey struct{}
type observation struct {
	ready chan struct{}
	value any
	err   error
}
type observations struct {
	sync.Mutex
	slots   chan struct{}
	entries map[string]*observation
}

// WithObservations shares read-only Git probes within one inventory cycle.
// Never carry this context across a mutation or into another refresh.
func WithObservations(ctx context.Context) context.Context {
	return context.WithValue(ctx, observationKey{}, &observations{entries: map[string]*observation{}, slots: make(chan struct{}, 8)})
}

func observe[T any](ctx context.Context, kind, path string, read func() (T, error)) (T, error) {
	cache, ok := ctx.Value(observationKey{}).(*observations)
	if !ok {
		return read()
	}
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		path = canonical
	}
	key := kind + "\x00" + path
	cache.Lock()
	entry, exists := cache.entries[key]
	if !exists {
		entry = &observation{ready: make(chan struct{})}
		cache.entries[key] = entry
	}
	cache.Unlock()
	if exists {
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-entry.ready:
		}
		return entry.value.(T), entry.err
	}
	value, err := read()
	entry.value, entry.err = value, err
	close(entry.ready)
	return value, err
}
