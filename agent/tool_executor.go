package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// toolExecutor handles the execution of LLM-requested tool calls.
// It fans out handlers across goroutines, enriches the context with
// a transport-aware ClientWriter when configured, and fires
// observability hooks for every call and result.
//
// Hook safety: the caller MUST pass serialised hooks (obtained via
// SerialisedHooks) to ExecuteToolCalls when tool calls run concurrently.
// Using the bare base hooks from parallel goroutines causes data races
// on shared hook writers.
type toolExecutor struct {
	tools       *ToolRegistry
	toolContext ToolContextDecorator
	hooks       Hooks // base hooks (used by SerialisedHooks)
}

// newToolExecutor builds a toolExecutor. The hooks are the BASE hooks
// (as configured in EngineConfig); the executor does not add
// serialisation. Callers should use SerialisedHooks() to wrap them
// with a per-turn mutex and pass the result to ExecuteToolCalls.
func newToolExecutor(tools *ToolRegistry, toolContext ToolContextDecorator, hooks Hooks) *toolExecutor {
	return &toolExecutor{
		tools:       tools,
		toolContext: toolContext,
		hooks:       hooks,
	}
}

// ---------------------------------------------------------------------------
// Public API (used by Conversation)
// ---------------------------------------------------------------------------

// ExecuteToolCalls runs every tool requested by the model, then appends
// all results to the session as tool-role messages.
//
// hooks MUST be the serialised hooks created by SerialisedHooks, not the
// bare base hooks, because tool goroutines run concurrently and would
// race on the shared hook writer otherwise.
func (ex *toolExecutor) ExecuteToolCalls(ctx context.Context, session SessionView, calls []ToolCall, events eventSender, hooks Hooks) {
	results := ex.runToolsConcurrently(ctx, session, calls, events, hooks)
	ex.appendToolResults(session, calls, results)
}

// SerialisedHooks wraps the executor's base hooks so that every callback
// is invoked under the provided mutex. This serialises hook invocations
// across concurrent tool goroutines, preventing torn writes when hooks
// write to a shared transport connection.
func (ex *toolExecutor) SerialisedHooks(turnMu *sync.Mutex) Hooks {
	base := ex.hooks
	return Hooks{
		OnToolCall: func(sid string, call ToolCall) {
			turnMu.Lock()
			defer turnMu.Unlock()
			base.FireToolCall(sid, call)
		},
		OnToolResult: func(sid string, call ToolCall, res event.ToolResult) {
			turnMu.Lock()
			defer turnMu.Unlock()
			base.FireToolResult(sid, call, res)
		},
		OnLLMResponse: func(sid string, r *LLMResponse) {
			turnMu.Lock()
			defer turnMu.Unlock()
			base.FireLLMResponse(sid, r)
		},
		OnError: func(sid string, err error) {
			turnMu.Lock()
			defer turnMu.Unlock()
			base.FireError(sid, err)
		},
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// runToolsConcurrently fans out tool handlers across goroutines and
// collects results in order. The order matches the calls slice so the
// LLM sees results in the same sequence it requested them.
func (ex *toolExecutor) runToolsConcurrently(ctx context.Context, session SessionView, calls []ToolCall, events eventSender, hooks Hooks) []string {
	results := make([]string, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(idx int, toolCall ToolCall) {
			defer wg.Done()
			results[idx] = ex.runSingleTool(ctx, session, toolCall, events, hooks)
		}(i, call)
	}
	wg.Wait()
	return results
}

// runSingleTool resolves the exec snippet, fires the OnToolCall hook,
// invokes the handler, fires the OnToolResult hook, emits events, and
// returns the result string.
//
// If a ToolContextDecorator is configured, it is invoked once here to
// enrich the context (e.g. install a client writer) before the handler
// runs. The orchestration loop itself stays transport-agnostic.
func (ex *toolExecutor) runSingleTool(ctx context.Context, session SessionView, call ToolCall, events eventSender, hooks Hooks) string {
	// Defense-in-depth: check if the tool is enabled for this session.
	// Even if the LLM somehow calls a disabled tool (e.g. from context window),
	// we reject it here without invoking the handler.
	if session != nil && !session.IsToolEnabled(call.Name) && call.Name != "context_compactify" {
		res := event.ErrorResult(errors.New("tool '" + call.Name + "' is disabled for this session"))
		fireToolCall(hooks, session.SessionID(), call)
		sendEngineEvent(events, event.ToolCallStarted{SessionID: session.SessionID(), ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		fireToolResult(hooks, session.SessionID(), call, res)
		sendEngineEvent(events, event.ToolCallFinished{SessionID: session.SessionID(), ID: call.ID, Name: call.Name, Arguments: call.Arguments, Result: res, Err: res.Err})
		return res.ForLLM()
	}

	call.ExecSnippet = ex.tools.ExecSnippet(call.Name, call.Arguments)
	fireToolCall(hooks, session.SessionID(), call)
	sendEngineEvent(events, event.ToolCallStarted{
		SessionID: session.SessionID(), ID: call.ID, Name: call.Name,
		Arguments: call.Arguments, ExecSnippet: call.ExecSnippet,
	})

	if ex.toolContext != nil {
		if decorated := ex.toolContext.Decorate(ctx, session.SessionID(), events); decorated != nil {
			ctx = decorated
		}
	}

	result := ex.tools.Execute(ctx, call.Name, call.Arguments)
	fireToolResult(hooks, session.SessionID(), call, result)
	sendEngineEvent(events, event.ToolCallFinished{
		SessionID: session.SessionID(), ID: call.ID, Name: call.Name,
		Arguments: call.Arguments, Result: result, Err: result.Err,
		ExecSnippet: call.ExecSnippet,
	})
	return result.ForLLM()
}

// appendToolResults adds tool-role messages to the session, one per call,
// preserving the order of the original calls slice.
func (ex *toolExecutor) appendToolResults(session SessionView, calls []ToolCall, results []string) {
	for i, call := range calls {
		session.Append(Message{
			Role:       RoleTool,
			Content:    results[i],
			ToolCallID: call.ID,
			Name:       call.Name,
		})
	}
}

// fireToolCall invokes the OnToolCall callback if set.
func fireToolCall(hooks Hooks, sessionID string, call ToolCall) {
	if hooks.OnToolCall != nil {
		hooks.OnToolCall(sessionID, call)
	}
}

// fireToolResult invokes the OnToolResult callback if set.
func fireToolResult(hooks Hooks, sessionID string, call ToolCall, result event.ToolResult) {
	if hooks.OnToolResult != nil {
		hooks.OnToolResult(sessionID, call, result)
	}
}
