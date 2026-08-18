package gateways

import (
	"context"
	"sync"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// activeTurn represents a currently running turn for a session. It
// reads from the turn's event channel and broadcasts every event to
// the namespace hub. The turn continues even if all clients disconnect.
type activeTurn struct {
	cancel    context.CancelFunc
	eventCh   <-chan event.EngineEvent
	done      chan struct{} // closed when the fan-out goroutine exits
	hub       *NamespaceHub
	sessionID string
	mu        sync.Mutex
}

// turnTracker manages active turns per session ID. It provides methods
// to start a turn, retrieve an active turn (for reconnection), and
// cancel a turn explicitly.
type turnTracker struct {
	turns sync.Map // sessionID -> *activeTurn
	hub   *NamespaceHub
}

func newTurnTracker(hub *NamespaceHub) *turnTracker {
	return &turnTracker{hub: hub}
}

// Start registers a new active turn for the given session, broadcasts a
// "turn_started" session event, and launches a fan-out goroutine that
// broadcasts every engine event to the namespace hub. The goroutine
// exits when the event channel closes, at which point a "turn_finished"
// session event is broadcast.
func (tt *turnTracker) Start(sessionID string, eventCh <-chan event.EngineEvent, cancel context.CancelFunc) *activeTurn {
	at := &activeTurn{
		cancel:    cancel,
		eventCh:   eventCh,
		done:      make(chan struct{}),
		hub:       tt.hub,
		sessionID: sessionID,
	}
	tt.turns.Store(sessionID, at)

	// Notify all clients that a turn started on this session.
	tt.hub.Broadcast(FrameResponse{
		Type:      "session",
		SessionID: sessionID,
		Event:     "turn_started",
	})

	go func() {
		defer close(at.done)
		defer tt.turns.Delete(sessionID)
		defer func() {
			// Notify all clients the turn finished.
			tt.hub.Broadcast(FrameResponse{
				Type:      "session",
				SessionID: sessionID,
				Event:     "turn_finished",
			})
		}()

		for evt := range eventCh {
			fr, ok := frameForEvent(evt)
			if !ok {
				continue
			}
			tt.hub.Broadcast(fr)
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
