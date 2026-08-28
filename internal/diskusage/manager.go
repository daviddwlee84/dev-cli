package diskusage

import (
	"context"
	"sync"
	"sync/atomic"
)

// ScanFunc is the measurement seam used by Manager tests.
type ScanFunc func(context.Context, Target) (Usage, error)

// Result is one streamed row measurement.
type Result struct {
	LoadID uint64
	Key    string
	Usage  Usage
	Err    error
}

// Load is a cancellable stream. Results closes after every unique target is
// delivered or the context is cancelled.
type Load struct {
	ID      uint64
	Results <-chan Result
}

// Manager bounds scans and shares one derived cache across CLI and TUI callers.
type Manager struct {
	Cache   *Cache
	Workers int
	Scan    ScanFunc

	next atomic.Uint64
	mu   sync.Mutex
	stop map[uint64]context.CancelFunc
}

// NewManager returns a manager with portable scanner defaults.
func NewManager(cache *Cache, workers int) *Manager {
	if workers <= 0 {
		workers = 2
	}
	return &Manager{Cache: cache, Workers: workers, Scan: Scan, stop: map[uint64]context.CancelFunc{}}
}

// Measure returns one cache hit or fresh scan.
func (m *Manager) Measure(ctx context.Context, target Target, force bool) (Usage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !force && m != nil && m.Cache != nil {
		if usage, ok, _ := m.Cache.Get(target); ok {
			return usage, nil
		}
	}
	scanner := ScanFunc(Scan)
	if m != nil && m.Scan != nil {
		scanner = m.Scan
	}
	usage, err := scanner(ctx, target)
	if err != nil {
		return Usage{}, err
	}
	if m != nil && m.Cache != nil {
		_ = m.Cache.Set(target, usage)
	}
	return usage, nil
}

// Start streams unique targets through a bounded worker pool.
func (m *Manager) Start(parent context.Context, targets []Target, force bool) Load {
	if parent == nil {
		parent = context.Background()
	}
	if m == nil {
		results := make(chan Result)
		close(results)
		return Load{Results: results}
	}
	id := m.next.Add(1)
	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	if m.stop == nil {
		m.stop = map[uint64]context.CancelFunc{}
	}
	m.stop[id] = cancel
	m.mu.Unlock()

	unique := make([]Target, 0, len(targets))
	seen := map[string]struct{}{}
	for _, target := range targets {
		key := targetKey(target)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		target.Key = key
		unique = append(unique, target)
	}
	results := make(chan Result, len(unique))
	load := Load{ID: id, Results: results}
	go m.run(ctx, cancel, load, unique, force, results)
	return load
}

func (m *Manager) run(ctx context.Context, cancel context.CancelFunc, load Load, targets []Target, force bool, results chan<- Result) {
	defer close(results)
	defer cancel()
	defer func() {
		m.mu.Lock()
		delete(m.stop, load.ID)
		m.mu.Unlock()
	}()

	jobs := make(chan Target)
	workers := m.Workers
	if workers <= 0 {
		workers = 2
	}
	if workers > len(targets) {
		workers = len(targets)
	}
	if workers == 0 {
		return
	}
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for {
				var target Target
				var ok bool
				select {
				case <-ctx.Done():
					return
				case target, ok = <-jobs:
					if !ok {
						return
					}
				}
				if ctx.Err() != nil {
					return
				}
				usage, err := m.Measure(ctx, target, force)
				if ctx.Err() != nil {
					return
				}
				result := Result{LoadID: load.ID, Key: target.Key, Usage: usage, Err: err}
				select {
				case results <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			close(jobs)
			wait.Wait()
			return
		}
		select {
		case jobs <- target:
		case <-ctx.Done():
			close(jobs)
			wait.Wait()
			return
		}
	}
	close(jobs)
	wait.Wait()
	if m.Cache != nil {
		_ = m.Cache.Save()
	}
}

// Cancel stops one active load. It is safe after completion.
func (m *Manager) Cancel(id uint64) {
	if m == nil || id == 0 {
		return
	}
	m.mu.Lock()
	cancel := m.stop[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Invalidate drops cached ownership layouts changed by a local mutation.
func (m *Manager) Invalidate(targets ...Target) error {
	if m == nil || m.Cache == nil {
		return nil
	}
	return m.Cache.Invalidate(targets...)
}
