package gateways

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

		"github.com/you/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// InProcessResponseReader tests
// ---------------------------------------------------------------------------

func TestInProcessResponseReader_DeliverAndAwait(t *testing.T) {
	rr := NewInProcessResponseReader()
	defer rr.Stop()
	ctx := context.Background()

	// Start a goroutine that waits for a response.
	done := make(chan *event.ClientResponse, 1)
	go func() {
		done <- rr.AwaitResponse(ctx, "req-1")
	}()

	// Give the goroutine time to register.
	time.Sleep(10 * time.Millisecond)

	// Deliver the response.
	resp := &event.ClientResponse{
		RequestID: "req-1",
		Result:    json.RawMessage(`"hello"`),
	}
	if !rr.Deliver(resp) {
		t.Fatal("Deliver returned false, expected true")
	}

	got := <-done
	if got == nil {
		t.Fatal("expected response, got nil")
	}
	if got.RequestID != "req-1" {
		t.Fatalf("expected request_id req-1, got %q", got.RequestID)
	}
	var s string
	if err := json.Unmarshal(got.Result, &s); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if s != "hello" {
		t.Fatalf("expected hello, got %q", s)
	}
}

func TestInProcessResponseReader_MissingRequestID(t *testing.T) {
	rr := NewInProcessResponseReader()
	defer rr.Stop()
	resp := &event.ClientResponse{RequestID: "nonexistent"}
	// Deliver to unknown request_id should buffer it (returns true).
	// The response can be retrieved if someone later calls AwaitResponse
	// with the same ID.
	if !rr.Deliver(resp) {
		t.Fatal("Deliver should return true (buffers the response)")
	}
	// Verify we can retrieve it later
	got := rr.AwaitResponse(context.Background(), "nonexistent")
	if got == nil {
		t.Fatal("expected to retrieve buffered response")
	}
}

func TestInProcessResponseReader_DeliverBeforeAwait(t *testing.T) {
	rr := NewInProcessResponseReader()
	defer rr.Stop()
	ctx := context.Background()

	// Deliver before anyone is listening.
	resp := &event.ClientResponse{
		RequestID: "req-2",
		Result:    json.RawMessage(`42`),
	}
	if !rr.Deliver(resp) {
		t.Fatal("Deliver returned false, expected true (Expect creates a channel)")
	}

	// Now await — should get the buffered response.
	got := rr.AwaitResponse(ctx, "req-2")
	if got == nil {
		t.Fatal("expected response, got nil")
	}
	var n float64
	if err := json.Unmarshal(got.Result, &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n != 42 {
		t.Fatalf("expected 42, got %v", n)
	}
}

func TestInProcessResponseReader_ContextCancelled(t *testing.T) {
	rr := NewInProcessResponseReader()
	defer rr.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	got := rr.AwaitResponse(ctx, "req-3")
	if got != nil {
		t.Fatal("expected nil on cancelled context, got response")
	}
}

func TestInProcessResponseReader_ConcurrentDelivery(t *testing.T) {
	rr := NewInProcessResponseReader()
	defer rr.Stop()
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan string, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		id := requestID(i)
		go func() {
			defer wg.Done()
			// Each goroutine awaits its own ID.
			resp := rr.AwaitResponse(ctx, id)
			if resp == nil {
				errs <- id + ": got nil"
				return
			}
			if resp.RequestID != id {
				errs <- id + ": wrong request_id: " + resp.RequestID
				return
			}
			var label string
			if err := json.Unmarshal(resp.Result, &label); err != nil {
				errs <- id + ": bad result: " + err.Error()
				return
			}
			if label != "done-"+id {
				errs <- id + ": unexpected result: " + label
			}
		}()
	}

	// Give goroutines time to register.
	time.Sleep(20 * time.Millisecond)

	// Deliver all responses.
	for i := 0; i < n; i++ {
		id := requestID(i)
		rr.Deliver(&event.ClientResponse{
			RequestID: id,
			Result:    mustMarshal(t, "done-"+id),
		})
	}

	wg.Wait()
	close(errs)

	var failures []string
	for e := range errs {
		failures = append(failures, e)
	}
	if len(failures) > 0 {
		t.Fatalf("concurrent test failures:\n%s", joinStrings(failures, "\n"))
	}
}

func TestInProcessResponseReader_BufferNoLeakOnLateResponse(t *testing.T) {
	rr := NewInProcessResponseReaderWithTTL(50 * time.Millisecond)
	defer rr.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Await will clean up pending entry, but will not remove a late buffer.
	_ = rr.AwaitResponse(ctx, "leaky-req")

	// Deliver a late response after the request was cancelled.
	rr.Deliver(&event.ClientResponse{RequestID: "leaky-req", Result: json.RawMessage(`"late"`)})

	// After the TTL, the sweep should clean it up.
	time.Sleep(100 * time.Millisecond)

	rr.mu.RLock()
	bufLen := len(rr.buffer)
	rr.mu.RUnlock()

	if bufLen != 0 {
		t.Fatalf("expected buffer to be empty after TTL, got %d entries", bufLen)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func requestID(i int) string { return "req-" + string(rune('a' + i)) }

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func joinStrings(ss []string, sep string) string {
	var result string
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
