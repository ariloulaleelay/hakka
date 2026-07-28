package gateways

import (
	"context"
	"sync"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// subscriber represents a single connected client that receives events
// from an active turn. When the writer fails (client disconnects), the
// done channel is closed so the subscriber's goroutine can exit.
type subscriber struct {
	writer frameWriter
	done   chan struct{} // closed when writer is removed due to write failure
}

// activeTurn represents a currently running turn for a session. It
// reads from the turn's event channel and fan-outs to all subscribed
// writers. The turn continues even if all subscribers disconnect.
type activeTurn struct {
	cancel context.CancelFunc // cancels the turn's context (LLM + tools)
	eventCh <-chan event.EngineEvent
	done    chan struct{} // closed when the fan-out goroutine exits
	subs    []*subscriber
	mu      sync.Mutex
}

// turnTracker manages active turns per session ID. It provides methods
// to start a turn, retrieve an active turn (for reconnection), and
// cancel a turn explicitly.
type turnTracker struct {
	turns sync.Map // sessionID -> *activeTurn
}

// Start registers a new active turn for the given session and launches
// a fan-out goroutine that distributes events to all subscribers. The
// goroutine exits when the event channel closes.
func (tt *turnTracker) Start(sessionID string, eventCh <-chan event.EngineEvent, cancel context.CancelFunc) *activeTurn {
	at := &activeTurn{
		cancel:  cancel,
		eventCh: eventCh,
		done:    make(chan struct{}),
	}
	tt.turns.Store(sessionID, at)

	go func() {
		defer close(at.done)
		defer tt.turns.Delete(sessionID)

		for evt := range eventCh {
			at.mu.Lock()
			var alive []*subscriber
			for _, sub := range at.subs {
				if processEvent(sub.writer, evt) {
					alive = append(alive, sub)
				} else {
					close(sub.done) // signal that this writer disconnected
				}
			}
			at.subs = alive
			at.mu.Unlock()
		}
	}()

	return at
}

func (tt *turnTracker) Get(sessionID string) *activeTurn {
	val, ok := tt.turns.Load(sessionID)
	if !ok {
		return nil
	}
	at, _ := val.(*activeTurn)
	return at
}

func (tt *turnTracker) Cancel(sessionID string) bool {
	val, ok := tt.turns.LoadAndDelete(sessionID)
	if !ok {
		return false
	}
	at, _ := val.(*activeTurn)
	if at != nil && at.cancel != nil {
		at.cancel()
	}
	return true
}

// Subscribe adds a writer to the active turn's subscriber list and
// blocks until one of:
//   - the turn finishes naturally (event channel closes)
//   - the writer fails (client disconnects)
//   - the context is cancelled
func (at *activeTurn) Subscribe(ctx context.Context, w frameWriter) {
	sub := &subscriber{writer: w, done: make(chan struct{})}

	at.mu.Lock()
	at.subs = append(at.subs, sub)
	at.mu.Unlock()

	select {
	case <-at.done:
	case <-sub.done:
	case <-ctx.Done():
	}
}

// ReplaceSubscriber removes any existing subscriber for the same
// connection (identified by ConnKey) and replaces it with the new
// writer. The old subscriber's done channel is closed so its blocking
// goroutine can exit. Then blocks like Subscribe until the turn finishes,
// the writer fails, or the context is cancelled.
//
// This prevents duplicate subscribers when a client re-subscribes to
// an active turn (e.g. via get_session) from a different goroutine
// on the same connection.
func (at *activeTurn) ReplaceSubscriber(ctx context.Context, w frameWriter) {
	key := w.ConnKey()
	sub := &subscriber{writer: w, done: make(chan struct{})}

	at.mu.Lock()
	// Remove any existing subscriber with the same connection key.
	var alive []*subscriber
	for _, s := range at.subs {
		if s.writer.ConnKey() == key {
			close(s.done) // unblock the old subscriber's goroutine
		} else {
			alive = append(alive, s)
		}
	}
	alive = append(alive, sub)
	at.subs = alive
	at.mu.Unlock()

	select {
	case <-at.done:
	case <-sub.done:
	case <-ctx.Done():
	}
}
