package inventory

import "context"

// Limiter bounds expensive local enrichment across collectors that would
// otherwise each create their own worker pool.
type Limiter struct {
	permits chan struct{}
}

// NewLimiter returns a context-aware concurrency bound.
func NewLimiter(limit int) *Limiter {
	if limit <= 0 {
		limit = 8
	}
	return &Limiter{permits: make(chan struct{}, limit)}
}

// Acquire waits for one permit and returns a matching release function.
func (l *Limiter) Acquire(ctx context.Context) (func(), bool) {
	if l == nil {
		return func() {}, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case l.permits <- struct{}{}:
		return func() { <-l.permits }, true
	case <-ctx.Done():
		return nil, false
	}
}
