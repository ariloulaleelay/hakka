package agent

import (
	"context"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// testCtxKey is a private context key type used in tests to avoid collisions.
type testCtxKey struct{}

// ---------------------------------------------------------------------------
// ToolContextDecoratorFunc
// ---------------------------------------------------------------------------

func TestToolContextDecoratorFunc_calls_the_wrapped_function(t *testing.T) {
	// Given a ToolContextDecoratorFunc that records what it received
	var capturedCtx context.Context
	var capturedSessionID string
	var capturedEvents chan<- event.EngineEvent

	events := make(chan event.EngineEvent, 1)
	originalCtx := context.WithValue(context.Background(), testCtxKey{}, "hello")

	decorator := ToolContextDecoratorFunc(func(ctx context.Context, sessionID string, ch chan<- event.EngineEvent) context.Context {
		capturedCtx = ctx
		capturedSessionID = sessionID
		capturedEvents = ch
		return ctx
	})

	// When Decorate is called
	_ = decorator.Decorate(originalCtx, "sess-abc", events)

	// Then the underlying function received the same arguments
	if capturedCtx != originalCtx {
		t.Error("underlying function was called with a different context")
	}
	if capturedSessionID != "sess-abc" {
		t.Errorf("expected sessionID 'sess-abc', got %q", capturedSessionID)
	}
	if capturedEvents != events {
		t.Error("underlying function was called with a different events channel")
	}
}

func TestToolContextDecoratorFunc_returns_the_context_from_the_wrapped_function(t *testing.T) {
	// Given a decorator that returns a modified context
	events := make(chan event.EngineEvent, 1)
	original := context.Background()
	expected := context.WithValue(original, testCtxKey{}, "modified")

	decorator := ToolContextDecoratorFunc(func(ctx context.Context, _ string, _ chan<- event.EngineEvent) context.Context {
		return expected
	})

	// When Decorate is called
	got := decorator.Decorate(original, "sid", events)

	// Then it returns the value from the wrapped function, not the input
	if got != expected {
		t.Error("Decorate did not return the context from the wrapped function")
	}
}

// ---------------------------------------------------------------------------
// EngineChannelClientDecorator
// ---------------------------------------------------------------------------

func TestEngineChannelClientDecorator_installs_an_EngineChannelWriter_with_matching_fields(t *testing.T) {
	// Given a background context
	ctx := context.Background()
	events := make(chan event.EngineEvent, 10)
	sessionID := "session-42"

	decorator := EngineChannelClientDecorator()

	// When the decorator is applied
	resultCtx := decorator.Decorate(ctx, sessionID, events)

	// Then the context contains a ClientWriter
	cw := event.ClientFromContext(resultCtx)
	if cw == nil {
		t.Fatal("expected a non-nil ClientWriter in the resulting context")
	}

	// And it is an *EngineChannelWriter with the correct fields
	ecw, ok := cw.(*event.EngineChannelWriter)
	if !ok {
		t.Fatalf("expected *event.EngineChannelWriter, got %T", cw)
	}
	if ecw.SessionID != sessionID {
		t.Errorf("expected SessionID %q, got %q", sessionID, ecw.SessionID)
	}
	if ecw.Events != events {
		t.Error("EngineChannelWriter has a different Events channel")
	}
}

func TestEngineChannelClientDecorator_preserves_the_existing_ResponseReader(t *testing.T) {
	// Given a context that already has a ResponseReader
	events := make(chan event.EngineEvent, 10)
	originalRR := &stubResponseReader{}
	ctx := event.ContextWithClient(context.Background(), nil, originalRR)

	decorator := EngineChannelClientDecorator()

	// When the decorator is applied
	resultCtx := decorator.Decorate(ctx, "s", events)

	// Then the ResponseReader is the same instance
	gotRR := event.ResponseReaderFromContext(resultCtx)
	if gotRR == nil {
		t.Fatal("expected a non-nil ResponseReader")
	}
	if gotRR != originalRR {
		t.Error("ResponseReader was replaced instead of preserved")
	}
}

func TestEngineChannelClientDecorator_handles_missing_ResponseReader_gracefully(t *testing.T) {
	// Given a context without a ResponseReader
	ctx := context.Background()
	events := make(chan event.EngineEvent, 10)

	decorator := EngineChannelClientDecorator()

	// When the decorator is applied
	resultCtx := decorator.Decorate(ctx, "s", events)

	// Then the ClientWriter is installed (no panic)
	cw := event.ClientFromContext(resultCtx)
	if cw == nil {
		t.Fatal("expected a non-nil ClientWriter even without a ResponseReader")
	}

	// And ResponseReader is nil
	rr := event.ResponseReaderFromContext(resultCtx)
	if rr != nil {
		t.Error("expected nil ResponseReader since none was in the input context")
	}
}

// ---------------------------------------------------------------------------
// stubs
// ---------------------------------------------------------------------------

type stubResponseReader struct{}

func (s *stubResponseReader) AwaitResponse(ctx context.Context, requestID string) *event.ClientResponse {
	return nil
}
