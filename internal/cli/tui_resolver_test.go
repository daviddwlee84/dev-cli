package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
)

func TestProjectRootResolverCachesCompleteTarget(t *testing.T) {
	resolver := newTUIProjectRootResolver(nil, t.Context())
	resolver.getwd = func() (string, error) { return "/worktrees/demo", nil }
	calls := 0
	want := agenttarget.Target{
		RepoName: "demo", RepoPath: "/repos/demo", CheckoutRoot: "/worktrees/demo", CommonDir: "/repos/demo/.git",
	}
	resolver.resolve = func(context.Context, string) (agenttarget.Target, error) {
		calls++
		return want, nil
	}
	first, err := resolver.ResolveTarget(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolver.ResolveTarget(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first != want || second != want {
		t.Fatalf("target calls=%d first=%+v second=%+v", calls, first, second)
	}
}

func TestProjectRootResolverIsNotPoisonedByCanceledWaiter(t *testing.T) {
	resolver := newTUIProjectRootResolver(nil, t.Context())
	resolver.getwd = func() (string, error) { return "/repo/nested", nil }
	started := make(chan struct{})
	release := make(chan struct{})
	resolver.resolve = func(ctx context.Context, _ string) (agenttarget.Target, error) {
		close(started)
		select {
		case <-ctx.Done():
			return agenttarget.Target{CheckoutRoot: "/repo/nested"}, nil
		case <-release:
			return agenttarget.Target{CheckoutRoot: "/repo"}, nil
		}
	}

	caller, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := resolver.Resolve(caller)
		first <- err
	}()
	<-started
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first waiter error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not return")
	}

	close(release)
	root, err := resolver.Resolve(t.Context())
	if err != nil || root != "/repo" {
		t.Fatalf("shared resolver root=%q err=%v", root, err)
	}
	if current, ok := resolver.Current(); !ok || current != "/repo" {
		t.Fatalf("Current = %q, %v", current, ok)
	}
}
