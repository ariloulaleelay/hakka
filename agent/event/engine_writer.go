package event

// EngineChannelWriter implements ClientWriter by translating outbound
// client frames into engine events on a channel.
//
// Rationale: when a tool wants to talk to the connected client (for
// example, the agent asking Neovim to evaluate a Lua expression), the
// naïve approach is to write directly to the transport. That causes a
// race when several tool goroutines run in parallel and all share one
// connection.
//
// EngineChannelWriter solves this by funneling the request into the
// engine's event channel instead. A single goroutine — the gateway's
// event loop — reads the channel and writes the corresponding frame
// to the wire. All outbound writes are therefore serialised by design,
// without any explicit locking.
//
// The writer is transport-agnostic: it only knows about EngineEvent
// values. Mapping a ClientRequestSent event back to a wire frame is
// the gateway's responsibility.
type EngineChannelWriter struct {
	SessionID string
	Events    chan<- EngineEvent
}

// WriteFrame converts a Frame carrying a ClientRequest into a
// ClientRequestSent event. Frames without a ClientRequest are dropped
// silently — this writer is only concerned with client-bound requests.
func (w *EngineChannelWriter) WriteFrame(f Frame) error {
	if f.ClientReq == nil {
		return nil
	}
	w.Events <- ClientRequestSent{
		SessionID: w.SessionID,
		RequestID: f.ClientReq.RequestID,
		Command:   f.ClientReq.Command,
	}
	return nil
}

// Compile-time interface check.
var _ ClientWriter = (*EngineChannelWriter)(nil)
