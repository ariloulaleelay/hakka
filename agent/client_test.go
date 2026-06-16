package agent

import (
	"context"
	"testing"

	"github.com/you/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// Context helper tests
// ---------------------------------------------------------------------------

type fakeClientWriter struct {
	frames []event.Frame
}

func (f *fakeClientWriter) WriteFrame(fr event.Frame) error {
	f.frames = append(f.frames, fr)
	return nil
}

type fakeResponseReader struct {
	responses map[string]*event.ClientResponse
}

func (f *fakeResponseReader) AwaitResponse(_ context.Context, requestID string) *event.ClientResponse {
	return f.responses[requestID]
}

func TestContextWithClient(t *testing.T) {
	cw := &fakeClientWriter{}
	rr := &fakeResponseReader{responses: map[string]*event.ClientResponse{}}
	ctx := event.ContextWithClient(context.Background(), cw, rr)

	gotCW := event.ClientFromContext(ctx)
	gotRR := event.ResponseReaderFromContext(ctx)

	if gotCW != cw {
		t.Fatal("ClientWriter mismatch")
	}
	if gotRR != rr {
		t.Fatal("ResponseReader mismatch")
	}
}

func TestContextWithClient_NilWhenNotSet(t *testing.T) {
	if cw := event.ClientFromContext(context.Background()); cw != nil {
		t.Fatal("expected nil ClientWriter")
	}
	if rr := event.ResponseReaderFromContext(context.Background()); rr != nil {
		t.Fatal("expected nil ResponseReader")
	}
}
