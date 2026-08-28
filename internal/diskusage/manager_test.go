package diskusage_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/diskusage"
)

func TestManagerStreamsUniqueTargetsWithBoundedConcurrency(t *testing.T) {
	var active, maximum, scans atomic.Int32
	manager := diskusage.NewManager(nil, 2)
	manager.Scan = func(ctx context.Context, target diskusage.Target) (diskusage.Usage, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		scans.Add(1)
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return diskusage.Usage{}, ctx.Err()
		}
		return diskusage.Usage{
			CheckoutBytes: int64(len(target.Checkout)), OwnedBytes: int64(len(target.Checkout)),
			TotalBytes: pointer(int64(len(target.Checkout))), Complete: true, MeasuredAt: time.Now().UTC(),
		}, nil
	}
	targets := []diskusage.Target{
		diskusage.Plain("/one"), diskusage.Plain("/two"), diskusage.Plain("/three"), diskusage.Plain("/one"),
	}
	load := manager.Start(context.Background(), targets, true)
	var results []diskusage.Result
	for result := range load.Results {
		results = append(results, result)
	}
	if len(results) != 3 || scans.Load() != 3 {
		t.Fatalf("streamed %d results from %d scans", len(results), scans.Load())
	}
	if maximum.Load() > 2 || maximum.Load() < 1 {
		t.Errorf("maximum concurrency = %d", maximum.Load())
	}
	for _, result := range results {
		if result.LoadID != load.ID || result.Key == "" || result.Err != nil {
			t.Errorf("result = %+v", result)
		}
	}
}

func TestManagerUsesCacheUnlessForced(t *testing.T) {
	now := time.Now().UTC()
	cache := diskusage.NewCache("", time.Hour)
	cache.Clock = func() time.Time { return now }
	manager := diskusage.NewManager(cache, 1)
	var scans atomic.Int32
	manager.Scan = func(context.Context, diskusage.Target) (diskusage.Usage, error) {
		scans.Add(1)
		return diskusage.Usage{CheckoutBytes: 7, OwnedBytes: 7, TotalBytes: pointer(7), Complete: true, MeasuredAt: now}, nil
	}
	target := diskusage.Plain("/cache")
	first, err := manager.Measure(context.Background(), target, false)
	if err != nil || first.Cached {
		t.Fatalf("first measurement = %+v, %v", first, err)
	}
	second, err := manager.Measure(context.Background(), target, false)
	if err != nil || !second.Cached || scans.Load() != 1 {
		t.Fatalf("cache hit = %+v, scans=%d, err=%v", second, scans.Load(), err)
	}
	if _, err := manager.Measure(context.Background(), target, true); err != nil || scans.Load() != 2 {
		t.Fatalf("forced scan count=%d err=%v", scans.Load(), err)
	}
}

func TestManagerCancellationStopsActiveLoad(t *testing.T) {
	started := make(chan struct{}, 1)
	manager := diskusage.NewManager(nil, 1)
	manager.Scan = func(ctx context.Context, target diskusage.Target) (diskusage.Usage, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return diskusage.Usage{}, ctx.Err()
	}
	load := manager.Start(context.Background(), []diskusage.Target{
		diskusage.Plain("/one"), diskusage.Plain("/two"),
	}, true)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scan did not start")
	}
	manager.Cancel(load.ID)
	select {
	case _, ok := <-load.Results:
		if ok {
			// A cancellation result is allowed; the stream still must close.
			select {
			case _, open := <-load.Results:
				if open {
					t.Fatal("load produced more results after cancellation")
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled load did not close")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled load did not close")
	}
	manager.Cancel(load.ID)
}

func TestManagerSurfacesScannerErrors(t *testing.T) {
	manager := diskusage.NewManager(nil, 1)
	manager.Scan = func(context.Context, diskusage.Target) (diskusage.Usage, error) {
		return diskusage.Usage{}, fmt.Errorf("scan failed")
	}
	load := manager.Start(context.Background(), []diskusage.Target{diskusage.Plain("/bad")}, true)
	result := <-load.Results
	if result.Err == nil || result.Key == "" {
		t.Fatalf("error result = %+v", result)
	}
}

func pointer(value int64) *int64 { return &value }
