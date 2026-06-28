package gateways

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// Unit tests for ReplaceSubscriber
// ---------------------------------------------------------------------------

// connKeyWriter is a frameWriter with a configurable ConnKey for testing.
type connKeyWriter struct {
	key string
}

func (w *connKeyWriter) Write(v FrameResponse) error { return nil }
func (w *connKeyWriter) ConnKey() string              { return w.key }

func TestActiveTurn_ReplaceSubscriber_RemovesOld(t *testing.T) {
	eventCh := make(chan event.EngineEvent)
	at := &activeTurn{
		eventCh: eventCh,
		done:    make(chan struct{}),
		cancel:  func() {},
	}

	// Add first subscriber with key "conn1"
	sub1 := &connKeyWriter{key: "conn1"}
	at.subs = []*subscriber{{writer: sub1, done: make(chan struct{})}}

	// Replace with another subscriber with same key
	sub2 := &connKeyWriter{key: "conn1"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go func() {
		at.ReplaceSubscriber(ctx, sub2)
	}()

	// Wait a moment for ReplaceSubscriber to process
	time.Sleep(50 * time.Millisecond)

	at.mu.Lock()
	if len(at.subs) != 1 {
		t.Fatalf("expected 1 subscriber after replace, got %d", len(at.subs))
	}
	if at.subs[0].writer != sub2 {
		t.Fatal("expected subscriber to be replaced with new writer")
	}
	at.mu.Unlock()
}

func TestActiveTurn_ReplaceSubscriber_DoesNotRemoveDifferent(t *testing.T) {
	eventCh := make(chan event.EngineEvent)
	at := &activeTurn{
		eventCh: eventCh,
		done:    make(chan struct{}),
		cancel:  func() {},
	}

	// Add first subscriber with key "conn1"
	sub1 := &connKeyWriter{key: "conn1"}
	at.subs = []*subscriber{{writer: sub1, done: make(chan struct{})}}

	// Replace with different key "conn2" — should add, not replace
	sub2 := &connKeyWriter{key: "conn2"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go func() {
		at.ReplaceSubscriber(ctx, sub2)
	}()

	time.Sleep(50 * time.Millisecond)

	at.mu.Lock()
	if len(at.subs) != 2 {
		t.Fatalf("expected 2 subscribers (different keys), got %d", len(at.subs))
	}
	at.mu.Unlock()
}

func TestActiveTurn_ReplaceSubscriber_NoLeakOnRepeatedReplace(t *testing.T) {
	at := &activeTurn{
		eventCh: make(chan event.EngineEvent),
		done:    make(chan struct{}),
		cancel:  func() {},
	}

	key := "conn1"
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Repeatedly replace subscriber 10 times — should still only have 1 subscriber
	for i := 0; i < 10; i++ {
		w := &connKeyWriter{key: key}
		go at.ReplaceSubscriber(ctx, w)
		time.Sleep(5 * time.Millisecond)
	}

	// Wait for all goroutines to either complete or timeout
	time.Sleep(100 * time.Millisecond)

	at.mu.Lock()
	count := len(at.subs)
	at.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 subscriber after 10 replaces, got %d", count)
	}
}

// Test duplicate subscriber prevention at the activeTurn level.
// This mirrors the real-world scenario: one goroutine Subscribes, another
// ReplaceSubscribes for the same connection key.
func TestActiveTurn_ReplaceSubscriber_PreventsDuplicate(t *testing.T) {
	ctx := context.Background()
	at := &activeTurn{
		eventCh: make(chan event.EngineEvent),
		done:    make(chan struct{}),
		cancel:  func() {},
	}

	w1 := &connKeyWriter{key: "same-conn"}
	w2 := &connKeyWriter{key: "same-conn"}

	var wg sync.WaitGroup

	// Simulate what happens in handleWithEngine
	wg.Add(1)
	go func() {
		defer wg.Done()
		at.Subscribe(ctx, w1)
	}()

	// Give Subscribe time to add w1
	time.Sleep(20 * time.Millisecond)

	// Now simulate what happens in handleJSONCommand (session_switch)
	wg.Add(1)
	go func() {
		defer wg.Done()
		at.ReplaceSubscriber(ctx, w2)
	}()

	// Give ReplaceSubscriber time to process
	time.Sleep(20 * time.Millisecond)

	// At this point, the old subscriber (w1) should have been removed
	// (its done channel closed, so Subscribe returned).
	// Only w2 should be in the subs list.
	at.mu.Lock()
	if len(at.subs) != 1 {
		t.Fatalf("expected 1 subscriber, got %d", len(at.subs))
	}
	if at.subs[0].writer != w2 {
		t.Fatal("expected w2 to be the only subscriber")
	}
	at.mu.Unlock()

	// Close done to unblock both goroutines
	close(at.done)
	wg.Wait()
}
