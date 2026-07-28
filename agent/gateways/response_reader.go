package gateways

import (
	"context"
	"sync"
	"time"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// InProcessResponseReader is an in-memory implementation of
// event.ResponseReader used by gateways. It routes incoming responses to
// the correct waiting tool goroutine.
//
// Deliver can be called before or after Expect/AwaitResponse. If called
// before, the response is buffered until the tool reads it.
//
// Buffered responses have a maximum lifetime (bufferTTL) to prevent memory
// leaks when a client sends a late response for a request that was already
// cancelled or timed out. A periodic sweep goroutine removes stale entries.
type InProcessResponseReader struct {
	mu        sync.RWMutex
	pending   map[string]chan *event.ClientResponse
	buffer    map[string]bufferedResponse
	bufferTTL time.Duration
	done      chan struct{}
}

type bufferedResponse struct {
	resp      *event.ClientResponse
	createdAt time.Time
}

func NewInProcessResponseReader() *InProcessResponseReader {
	return newInProcessResponseReader(30 * time.Second)
}

func NewInProcessResponseReaderWithTTL(ttl time.Duration) *InProcessResponseReader {
	return newInProcessResponseReader(ttl)
}

func newInProcessResponseReader(ttl time.Duration) *InProcessResponseReader {
	r := &InProcessResponseReader{
		pending:   make(map[string]chan *event.ClientResponse),
		buffer:    make(map[string]bufferedResponse),
		bufferTTL: ttl,
		done:      make(chan struct{}),
	}
	go r.sweepLoop()
	return r
}

func (reader *InProcessResponseReader) sweepLoop() {
	ticker := time.NewTicker(reader.bufferTTL / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			reader.sweep()
		case <-reader.done:
			return
		}
	}
}

func (reader *InProcessResponseReader) sweep() {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	now := time.Now()
	for id, br := range reader.buffer {
		if now.Sub(br.createdAt) > reader.bufferTTL {
			delete(reader.buffer, id)
		}
	}
}

func (reader *InProcessResponseReader) Stop() {
	close(reader.done)
}

// Expect creates a channel for the given requestID and stores it.
// Returns the channel the tool will read from. If a response was already
// delivered via Deliver, it is written into the channel immediately.
func (reader *InProcessResponseReader) Expect(requestID string) chan *event.ClientResponse {
	reader.mu.Lock()
	defer reader.mu.Unlock()

	// Check buffer for pre-delivered response
	if buf, ok := reader.buffer[requestID]; ok {
		delete(reader.buffer, requestID)
		ch := make(chan *event.ClientResponse, 1)
		ch <- buf.resp
		return ch
	}

	ch := make(chan *event.ClientResponse, 1)
	reader.pending[requestID] = ch
	return ch
}

// Deliver sends a response to the tool waiting for the given requestID.
// If no tool is waiting yet, the response is buffered with a TTL.
func (reader *InProcessResponseReader) Deliver(resp *event.ClientResponse) bool {
	reader.mu.Lock()
	defer reader.mu.Unlock()

	ch, hasWaiter := reader.pending[resp.RequestID]
	if hasWaiter {
		delete(reader.pending, resp.RequestID)
		select {
		case ch <- resp:
		default:
		}
		return true
	}

	// No one waiting yet — buffer the response with a timestamp so the
	// sweep goroutine can clean it up if it's never claimed.
	reader.buffer[resp.RequestID] = bufferedResponse{
		resp:      resp,
		createdAt: time.Now(),
	}
	return true
}

func (reader *InProcessResponseReader) Cleanup(requestID string) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	delete(reader.pending, requestID)
	// Also clean up any buffered response that may have arrived between
	// Expect and Cleanup.
	delete(reader.buffer, requestID)
}

// AwaitResponse implements event.ResponseReader. It blocks until the
// response arrives or the context is cancelled.
func (reader *InProcessResponseReader) AwaitResponse(ctx context.Context, requestID string) *event.ClientResponse {
	ch := reader.Expect(requestID)
	defer reader.Cleanup(requestID)
	select {
	case resp := <-ch:
		return resp
	case <-ctx.Done():
		return nil
	}
}