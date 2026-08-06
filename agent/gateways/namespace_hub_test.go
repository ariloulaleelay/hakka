package gateways

import (
	"fmt"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// NamespaceHub unit tests
// ---------------------------------------------------------------------------

func newSpyWriter(name string) *spyWriter {
	return &spyWriter{name: name, connKey: fmt.Sprintf("spy:%s", name)}
}

// failingWriter always fails on write — used to test dead-writer removal.
type failingWriter struct{}

func (f *failingWriter) Write(_ FrameResponse) error { return errFakeWrite }
func (f *failingWriter) ConnKey() string             { return "fail" }

var errFakeWrite = errFakeWriteSentinel{}

type errFakeWriteSentinel struct{}

func (errFakeWriteSentinel) Error() string { return "fake write error" }

func TestHubSubscribeBroadcast(t *testing.T) {
	hub := NewNamespaceHub("test-hub-1")

	spyA := newSpyWriter("a")
	spyB := newSpyWriter("b")

	hub.Subscribe(spyA)
	hub.Subscribe(spyB)

	fr := FrameResponse{Type: "session", SessionID: "s1", Event: "session_create"}
	hub.Broadcast(fr)

	// Both receive.
	if n := len(spyA.Frames()); n != 1 {
		t.Fatalf("client A: expected 1 frame, got %d", n)
	}
	if n := len(spyB.Frames()); n != 1 {
		t.Fatalf("client B: expected 1 frame, got %d", n)
	}
	if spyA.Frames()[0].Event != "session_create" {
		t.Fatalf("client A: wrong event: %q", spyA.Frames()[0].Event)
	}
}

func TestHubBroadcastExcept(t *testing.T) {
	hub := NewNamespaceHub("test-hub-2")

	spyA := newSpyWriter("a")
	spyB := newSpyWriter("b")

	hub.Subscribe(spyA)
	hub.Subscribe(spyB)

	fr := FrameResponse{Type: "session", SessionID: "s1", Event: "session_create"}
	hub.BroadcastExcept(fr, spyA)

	// B receives, A doesn't.
	if n := len(spyA.Frames()); n != 0 {
		t.Fatalf("client A: expected 0 frames (excepted), got %d", n)
	}
	if n := len(spyB.Frames()); n != 1 {
		t.Fatalf("client B: expected 1 frame, got %d", n)
	}
}

func TestHubDeadWriterRemoved(t *testing.T) {
	hub := NewNamespaceHub("test-hub-3")

	fail := &failingWriter{}
	spy := newSpyWriter("s")

	hub.Subscribe(fail)
	hub.Subscribe(spy)

	fr := FrameResponse{Type: "delta", SessionID: "s1", Text: "hello"}
	hub.Broadcast(fr)

	// Spy receives.
	if n := len(spy.Frames()); n != 1 {
		t.Fatalf("spy: expected 1 frame, got %d", n)
	}

	// Dead writer removed — no longer in subs.
	if n := hub.Len(); n != 1 {
		t.Fatalf("expected 1 alive subscriber, got %d", n)
	}

	// Second broadcast reaches only spy.
	hub.Broadcast(FrameResponse{Type: "done", SessionID: "s1"})
	if n := len(spy.Frames()); n != 2 {
		t.Fatalf("spy: expected 2 frames after second broadcast, got %d", n)
	}
}

func TestHubUnsubscribe(t *testing.T) {
	hub := NewNamespaceHub("test-hub-4")

	spyA := newSpyWriter("a")
	spyB := newSpyWriter("b")

	hub.Subscribe(spyA)
	hub.Subscribe(spyB)
	hub.Unsubscribe(spyA)

	hub.Broadcast(FrameResponse{Type: "delta", Text: "hi"})

	if n := len(spyA.Frames()); n != 0 {
		t.Fatalf("client A: expected 0 frames after unsub, got %d", n)
	}
	if n := len(spyB.Frames()); n != 1 {
		t.Fatalf("client B: expected 1 frame, got %d", n)
	}
}

func TestHubConcurrentBroadcast(t *testing.T) {
	hub := NewNamespaceHub("test-hub-5")
	spies := make([]*spyWriter, 10)
	for i := range spies {
		spies[i] = newSpyWriter(fmt.Sprintf("s%d", i))
		hub.Subscribe(spies[i])
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hub.Broadcast(FrameResponse{Type: "delta", Text: "x"})
		}()
	}
	wg.Wait()

	// Each spy received 5 frames.
	for i, s := range spies {
		if n := len(s.Frames()); n != 5 {
			t.Fatalf("spy %d: expected 5 frames, got %d", i, n)
		}
	}
}
