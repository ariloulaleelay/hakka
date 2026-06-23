package event

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// ToolResult
// ---------------------------------------------------------------------------

func TestToolResult_for_a_successful_invocation(t *testing.T) {
	// Given a tool that succeeds with output "result-data"
	result := SuccessResult("result-data")

	// Then it is not an error
	if result.IsError() {
		t.Errorf("IsError() = true, want false")
	}

	// And ForLLM returns the raw output
	if got := result.ForLLM(); got != "result-data" {
		t.Errorf(`ForLLM() = %q, want "result-data"`, got)
	}
}

func TestToolResult_for_a_failed_invocation(t *testing.T) {
	// Given a tool that fails with error "something went wrong"
	cause := errors.New("something went wrong")
	result := ErrorResult(cause)

	// Then it is an error
	if !result.IsError() {
		t.Errorf("IsError() = false, want true")
	}

	// And ForLLM prepends "Error: "
	if got := result.ForLLM(); got != "Error: something went wrong" {
		t.Errorf(`ForLLM() = %q, want "Error: something went wrong"`, got)
	}
}

// ---------------------------------------------------------------------------
// Context — CWD
// ---------------------------------------------------------------------------

func TestCWD_written_to_context_can_be_read_back(t *testing.T) {
	// Given a context with CWD "/home/user"
	ctx := ContextWithCWD(context.Background(), "/home/user")

	// When CWD is extracted
	cwd := CWDFromContext(ctx)

	// Then it matches the stored value
	if cwd != "/home/user" {
		t.Errorf(`CWDFromContext() = %q, want "/home/user"`, cwd)
	}
}

func TestCWD_read_from_context_without_CWD_returns_empty(t *testing.T) {
	// Given a context without CWD
	ctx := context.Background()

	// When CWD is extracted
	cwd := CWDFromContext(ctx)

	// Then it is empty
	if cwd != "" {
		t.Errorf(`CWDFromContext() = %q, want ""`, cwd)
	}
}

// ---------------------------------------------------------------------------
// Context — namespace
// ---------------------------------------------------------------------------

func TestNamespace_written_to_context_can_be_read_back(t *testing.T) {
	// Given a context with namespace "tcp"
	ctx := ContextWithNamespace(context.Background(), "tcp")

	// When namespace is extracted
	ns := NamespaceFromContext(ctx)

	// Then it matches the stored value
	if ns != "tcp" {
		t.Errorf(`NamespaceFromContext() = %q, want "tcp"`, ns)
	}
}

func TestNamespace_read_from_context_without_namespace_returns_empty(t *testing.T) {
	// Given a context without namespace
	ctx := context.Background()

	// When namespace is extracted
	ns := NamespaceFromContext(ctx)

	// Then it is empty
	if ns != "" {
		t.Errorf(`NamespaceFromContext() = %q, want ""`, ns)
	}
}

// ---------------------------------------------------------------------------
// Context — ClientWriter & ResponseReader
// ---------------------------------------------------------------------------

// stubClientWriter implements ClientWriter for testing.
type stubClientWriter struct{}

func (stubClientWriter) WriteFrame(Frame) error { return nil }

// stubResponseReader implements ResponseReader for testing.
type stubResponseReader struct{}

func (stubResponseReader) AwaitResponse(context.Context, string) *ClientResponse { return nil }

func TestClientWriter_and_ResponseReader_written_to_context_can_be_read_back(t *testing.T) {
	// Given a context with a ClientWriter and a ResponseReader
	cw := stubClientWriter{}
	rr := stubResponseReader{}
	ctx := ContextWithClient(context.Background(), cw, rr)

	// When both are extracted
	gotCW := ClientFromContext(ctx)
	gotRR := ResponseReaderFromContext(ctx)

	// Then they are the same instances
	if gotCW != cw {
		t.Errorf("ClientFromContext() returned a different writer")
	}
	if gotRR != rr {
		t.Errorf("ResponseReaderFromContext() returned a different reader")
	}
}

func TestClientWriter_read_from_context_without_client_returns_nil(t *testing.T) {
	// Given a context without client info
	ctx := context.Background()

	// When extracted
	cw := ClientFromContext(ctx)
	rr := ResponseReaderFromContext(ctx)

	// Then both are nil
	if cw != nil {
		t.Errorf("ClientFromContext() = %v, want nil", cw)
	}
	if rr != nil {
		t.Errorf("ResponseReaderFromContext() = %v, want nil", rr)
	}
}

// ---------------------------------------------------------------------------
// EngineChannelWriter
// ---------------------------------------------------------------------------

func TestEngineChannelWriter_emits_ClientRequestSent_for_a_frame_with_a_ClientRequest(t *testing.T) {
	// Given an EngineChannelWriter with a buffered channel
	ch := make(chan EngineEvent, 1)
	w := &EngineChannelWriter{
		SessionID: "session-1",
		Events:    ch,
	}

	// When WriteFrame is called with a frame carrying a ClientRequest
	frame := Frame{
		SessionID: "session-1",
		ClientReq: &ClientRequest{
			RequestID: "req-1",
			Command:   `vim.api.nvim_eval("1+1")`,
		},
	}
	if err := w.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame() returned error: %v", err)
	}

	// Then a ClientRequestSent event is sent to the channel
	select {
	case ev := <-ch:
		evt, ok := ev.(ClientRequestSent)
		if !ok {
			t.Fatalf("expected ClientRequestSent, got %T", ev)
		}
		if evt.SessionID != "session-1" {
			t.Errorf("SessionID = %q, want %q", evt.SessionID, "session-1")
		}
		if evt.RequestID != "req-1" {
			t.Errorf("RequestID = %q, want %q", evt.RequestID, "req-1")
		}
		if evt.Command != `vim.api.nvim_eval("1+1")` {
			t.Errorf("Command = %q, want %q", evt.Command, `vim.api.nvim_eval("1+1")`)
		}
	default:
		t.Fatal("expected an event on the channel, but nothing was sent")
	}
}

func TestEngineChannelWriter_drops_a_frame_without_a_ClientRequest(t *testing.T) {
	// Given an EngineChannelWriter with a buffered channel
	ch := make(chan EngineEvent, 1)
	w := &EngineChannelWriter{
		SessionID: "session-1",
		Events:    ch,
	}

	// When WriteFrame is called with a frame that has no ClientRequest
	frame := Frame{
		SessionID: "session-1",
		Event:     "some_event",
		Data:      map[string]any{"key": "value"},
		// ClientReq is nil
	}
	if err := w.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame() returned error: %v", err)
	}

	// Then nothing is sent to the channel
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event on the channel: %T %+v", ev, ev)
	default:
		// OK — nothing was sent
	}
}
