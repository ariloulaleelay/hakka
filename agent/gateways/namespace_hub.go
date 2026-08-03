package gateways

import (
	"sync"
)

// NamespaceHub is the per-namespace event broadcast hub. Every connected
// client registers its frameWriter here. All turn events (delta, tool,
// usage, done, req, renamed) and session lifecycle events (session_create,
// renamed, session_delete) are broadcast to every subscriber in the
// namespace. Clients filter by session_id on the receiving side.
//
// The hub also owns the namespace's turnTracker, ensuring consistent
// in_flight status and cancellation across all transports sharing the
// same namespace (e.g. standalone WS + webfront on "ws").
type NamespaceHub struct {
	ns   string
	mu   sync.Mutex
	subs []frameWriter // active subscribers

	turns *turnTracker
}

// NewNamespaceHub creates a fresh hub for the given namespace.
func NewNamespaceHub(ns string) *NamespaceHub {
	h := &NamespaceHub{ns: ns}
	h.turns = newTurnTracker(h)
	return h
}

// Turns returns the hub's shared turn tracker.
func (h *NamespaceHub) Turns() *turnTracker { return h.turns }

// Subscribe registers a writer to receive all broadcasts. Idempotent:
// calling twice with the same ConnKey replaces the existing entry.
func (h *NamespaceHub) Subscribe(w frameWriter) {
	key := w.ConnKey()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, sub := range h.subs {
		if sub.ConnKey() == key {
			h.subs[i] = w
			return
		}
	}
	h.subs = append(h.subs, w)
}

// Unsubscribe removes the writer. Safe to call on a writer that was
// never subscribed.
func (h *NamespaceHub) Unsubscribe(w frameWriter) {
	key := w.ConnKey()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, sub := range h.subs {
		if sub.ConnKey() == key {
			h.subs = append(h.subs[:i], h.subs[i+1:]...)
			return
		}
	}
}

// Broadcast sends a frame to all registered writers. Dead writers are
// removed automatically on write failure.
func (h *NamespaceHub) Broadcast(fr FrameResponse) {
	h.mu.Lock()
	alive := make([]frameWriter, 0, len(h.subs))
	for _, w := range h.subs {
		if w.Write(fr) == nil {
			alive = append(alive, w)
		}
	}
	h.subs = alive
	h.mu.Unlock()
}

// BroadcastExcept sends a frame to all registered writers except the
// specified one (identified by ConnKey). The requester receives its
// response directly (via writeCommandResult or similar); this prevents
// duplicate delivery.
func (h *NamespaceHub) BroadcastExcept(fr FrameResponse, except frameWriter) {
	if except == nil {
		h.Broadcast(fr)
		return
	}
	exceptKey := except.ConnKey()
	h.mu.Lock()
	alive := make([]frameWriter, 0, len(h.subs))
	for _, w := range h.subs {
		if w.ConnKey() == exceptKey {
			alive = append(alive, w) // keep alive, just skip
			continue
		}
		if w.Write(fr) == nil {
			alive = append(alive, w)
		}
	}
	h.subs = alive
	h.mu.Unlock()
}

// Len returns the number of active subscribers (for tests).
func (h *NamespaceHub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
