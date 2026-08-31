package cli

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProjectRootResolverIsNotPoisonedByCanceledWaiter(t *testing.T) {
	resolver := newTUIProjectRootResolver(nil, t.Context())
	resolver.getwd = func() (string, error) { return "/repo/nested", nil }
	started := make(chan struct{})
	release := make(chan struct{})
	resolver.projectRoot = func(ctx context.Context, _ string) string {
		close(started)
		select {
		case <-ctx.Done():
			return "/repo/nested"
		case <-release:
			return "/repo"
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
