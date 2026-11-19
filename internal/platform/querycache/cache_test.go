package querycache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHitExpiryIsolationAndNumberPrecision(t *testing.T) {
	clock := time.Now()
	c := &Cache{now: func() time.Time { return clock }}
	calls := 0
	fetch := func(context.Context) (map[string]any, time.Duration, error) {
		calls++
		return map[string]any{"id": json.Number("7146244493748996128"), "nested": []any{map[string]any{"name": "original"}}}, time.Minute, nil
	}
	key := Key("provider", "query", "1", "secret")
	a, err := Load(context.Background(), c, key, fetch)
	if err != nil {
		t.Fatal(err)
	}
	a["nested"].([]any)[0].(map[string]any)["name"] = "edited"
	b, err := Load(context.Background(), c, key, fetch)
	if err != nil || calls != 1 || b["id"] != json.Number("7146244493748996128") || b["nested"].([]any)[0].(map[string]any)["name"] != "original" {
		t.Fatal("cache hit fetched upstream, aliased a caller or rounded an ID", b, calls, err)
	}
	clock = clock.Add(time.Minute)
	if _, err := Load(context.Background(), c, key, fetch); err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
	for _, k := range []string{Key("provider", "other", "1", "secret"), Key("provider", "query", "2", "secret"), Key("provider", "query", "1", "new-secret")} {
		if _, err := Load(context.Background(), c, k, fetch); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 5 || strings.Contains(key, "secret") || Key("a", "bc") == Key("ab", "c") {
		t.Fatal("query/page/credential isolation failed", calls)
	}
}

func TestErrorsEmptyResultsAndOptOut(t *testing.T) {
	c := &Cache{}
	calls := 0
	fetch := func(context.Context) ([]string, time.Duration, error) {
		calls++
		if calls == 1 {
			return nil, time.Hour, errors.New("unavailable")
		}
		return []string{}, time.Minute, nil
	}
	if _, err := Load(context.Background(), c, "key", fetch); err == nil {
		t.Fatal("error lost")
	}
	for range 2 {
		if rows, err := Load(context.Background(), c, "key", fetch); err != nil || rows == nil || len(rows) != 0 {
			t.Fatal(rows, err)
		}
	}
	if calls != 2 {
		t.Fatal("failure cached or empty success not cached", calls)
	}
	for range 2 {
		_, err := Load(context.Background(), c, "live", func(context.Context) (int, time.Duration, error) { calls++; return 1, 0, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 4 {
		t.Fatal("zero TTL did not opt out", calls)
	}
}

func TestConcurrentMissesCancelIndependently(t *testing.T) {
	c := &Cache{}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	fetch := func(ctx context.Context) (string, time.Duration, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return "result", time.Hour, nil
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := Load(ctx, c, "key", fetch); first <- err }()
	<-started
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			value, err := Load(context.Background(), c, "key", fetch)
			if err != nil || value != "result" {
				t.Errorf("waiter lost shared result: %q %v", value, err)
			}
		})
	}
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.flights["key"].waiters == 13 })
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate upstream requests", calls.Load())
	}
}

func TestLastCancellationStopsWorkAndLateResultsCannotOverwrite(t *testing.T) {
	c := &Cache{}
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Load(ctx, c, "key", func(ctx context.Context) (string, time.Duration, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release // Simulate an upstream that ignores cancellation until later.
			return "old", time.Hour, nil
		})
		done <- err
	}()
	<-started
	c.mu.Lock()
	oldFlight := c.flights["key"]
	c.mu.Unlock()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("last waiter did not cancel upstream")
	}
	fetch := func(context.Context) (string, time.Duration, error) { return "new", time.Hour, nil }
	if v, err := Load(context.Background(), c, "key", fetch); err != nil || v != "new" {
		t.Fatal(v, err)
	}
	close(release)
	<-oldFlight.done
	if v, err := Load(context.Background(), c, "key", fetch); err != nil || v != "new" {
		t.Fatal("late result overwrote cache", v, err)
	}
	pre, stop := context.WithCancel(context.Background())
	stop()
	if _, err := Load(pre, c, "key", fetch); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled caller received cache", err)
	}
}

func TestCapacityAndLRUEviction(t *testing.T) {
	c := &Cache{}
	fetch := func(context.Context) (int, time.Duration, error) { return 1, time.Hour, nil }
	for i := range defaultEntries {
		if _, err := Load(context.Background(), c, fmt.Sprint(i), fetch); err != nil {
			t.Fatal(err)
		}
	}
	Load(context.Background(), c, "0", fetch)
	Load(context.Background(), c, "overflow", fetch)
	if c.lru.Len() != defaultEntries || c.entries["0"] == nil || c.entries["1"] != nil {
		t.Fatal("LRU capacity not respected")
	}
	large := strings.Repeat("a", defaultBytes/2)
	for i := range 4 {
		Load(context.Background(), c, fmt.Sprint("large", i), func(context.Context) (string, time.Duration, error) { return large, time.Hour, nil })
	}
	if c.bytes > defaultBytes || c.lru.Len() != 1 {
		t.Fatal("byte budget not respected", c.bytes, c.lru.Len())
	}
}

func TestInFlightCapacityOverflowStillCopiesResults(t *testing.T) {
	c := &Cache{flights: map[string]*flight{}}
	for i := range defaultEntries {
		c.flights[fmt.Sprint(i)] = &flight{}
	}
	original := map[string]any{"value": "original"}
	result, err := Load(t.Context(), c, "overflow", func(context.Context) (map[string]any, time.Duration, error) { return original, time.Hour, nil })
	if err != nil {
		t.Fatal(err)
	}
	result["value"] = "edited"
	if original["value"] != "original" || len(c.flights) != defaultEntries || c.lru.Len() != 0 {
		t.Fatal("overflow lost isolation or exceeded capacity")
	}
}

func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
