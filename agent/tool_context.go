package agent

import (
	"context"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// ToolContextDecorator is an extension point that lets gateways enrich
// the context handed to every tool handler — typically to install a
// ClientWriter so tools can call back into the connected client.
//
// Conversation calls Decorate once per tool invocation, right before
// invoking the handler. The decorator may add values to the context
// (e.g. a ClientWriter) or return it unchanged. Returning nil is
// equivalent to returning the input context.
//
// Keeping this concern behind an interface means the orchestration
// loop in Conversation does not have to know which transport is
// connected or what frames that transport understands. The default
// (a nil decorator) is a no-op, which is correct for transports that
// have no client-bound communication.
type ToolContextDecorator interface {
	Decorate(ctx context.Context, sessionID string, events chan<- event.EngineEvent) context.Context
}

// ToolContextDecoratorFunc adapts a plain function to the
// ToolContextDecorator interface.
type ToolContextDecoratorFunc func(ctx context.Context, sessionID string, events chan<- event.EngineEvent) context.Context

// Decorate calls the underlying function.
func (f ToolContextDecoratorFunc) Decorate(ctx context.Context, sessionID string, events chan<- event.EngineEvent) context.Context {
	return f(ctx, sessionID, events)
}

// EngineChannelClientDecorator returns a decorator that installs an
// EngineChannelWriter as the ClientWriter for each tool invocation.
// The ResponseReader already in the context is preserved.
//
// This is the decorator gateways should use when their tools need to
// send client-bound requests (e.g. Neovim Lua evaluation) and those
// requests must be serialised through the engine event loop to avoid
// races on the shared connection writer.
func EngineChannelClientDecorator() ToolContextDecorator {
	return ToolContextDecoratorFunc(func(ctx context.Context, sessionID string, events chan<- event.EngineEvent) context.Context {
		rr := event.ResponseReaderFromContext(ctx)
		cw := &event.EngineChannelWriter{SessionID: sessionID, Events: events}
		return event.ContextWithClient(ctx, cw, rr)
	})
}
