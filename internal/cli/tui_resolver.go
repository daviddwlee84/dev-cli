package cli

import (
	"context"
	"os"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/perftrace"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

func resolverOutcome(err error) perftrace.Outcome {
	if err != nil {
		return perftrace.OutcomeFailed
	}
	return perftrace.OutcomeSuccess
}

// tuiRuntimeResolver moves runtime auto-detection off the first-view path while
// sharing exactly one result across inventory loads and actions.
type tuiRuntimeResolver struct {
	trace   *perftrace.Recorder
	resolve func() runtime.Runtime
	once    sync.Once
	ready   chan struct{}
	rt      runtime.Runtime
}

func newTUIRuntimeResolver(app *App) *tuiRuntimeResolver {
	snapshot := *app
	return &tuiRuntimeResolver{
		trace: app.trace, resolve: snapshot.Runtime, ready: make(chan struct{}),
	}
}

func (r *tuiRuntimeResolver) Resolve(ctx context.Context) (runtime.Runtime, error) {
	r.once.Do(func() {
		go func() {
			finish := r.trace.Start(perftrace.TUIRuntimeResolve, perftrace.Fields{})
			r.rt = r.resolve()
			finish(perftrace.OutcomeSuccess)
			close(r.ready)
		}()
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.ready:
		return r.rt, nil
	}
}

// tuiProjectRootResolver keeps Git project-root discovery lazy and shared by
// SKILLS reads/actions.
type tuiProjectRootResolver struct {
	trace   *perftrace.Recorder
	ctx     context.Context
	getwd   func() (string, error)
	resolve func(context.Context, string) (agenttarget.Target, error)
	once    sync.Once
	ready   chan struct{}
	target  agenttarget.Target
	err     error
}

func newTUIProjectRootResolver(trace *perftrace.Recorder, ctx context.Context) *tuiProjectRootResolver {
	if ctx == nil {
		ctx = context.Background()
	}
	return &tuiProjectRootResolver{
		trace: trace, ctx: ctx, getwd: os.Getwd, resolve: agenttarget.Current,
		ready: make(chan struct{}),
	}
}

func (r *tuiProjectRootResolver) ResolveTarget(ctx context.Context) (agenttarget.Target, error) {
	r.once.Do(func() {
		go func() {
			finish := r.trace.Start(perftrace.TUIProjectRootResolve, perftrace.Fields{})
			cwd, err := r.getwd()
			if err == nil {
				r.target, err = r.resolve(r.ctx, cwd)
			}
			r.err = err
			finish(resolverOutcome(err))
			close(r.ready)
		}()
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return agenttarget.Target{}, ctx.Err()
	case <-r.ready:
		return r.target, r.err
	}
}

func (r *tuiProjectRootResolver) Resolve(ctx context.Context) (string, error) {
	target, err := r.ResolveTarget(ctx)
	return target.CheckoutRoot, err
}

func (r *tuiProjectRootResolver) Current() (string, bool) {
	select {
	case <-r.ready:
		return r.target.CheckoutRoot, r.err == nil
	default:
		return "", false
	}
}
