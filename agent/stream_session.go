package agent

import (
	"context"
	"strings"

	"github.com/you/hakka/agent/event"
)

// StreamSession streams assistant deltas for a turn, handling tool calls
// inline without falling back to a non-streaming Complete() call.
//
// For each streaming round:
//  1. Stream text deltas to the client
//  2. If the model requests tools, execute them, append results to the
//     session, and start a new streaming round
//  3. Continue until the model returns text-only (no tools) or an error
//
// Callers receive typed EngineEvents from the returned channel, including
// TextDelta for each content chunk, ToolCallStarted/ToolCallFinished, and
// TurnFinished when the turn ends.
//
// StreamSession wraps a Conversation and reuses its shared turn lifecycle
// (runTurnWithStep) with a streaming step function, avoiding duplication
// of session persistence and event emission logic.
//
// Namespace is used directly by StreamSession (not delegated to the
// wrapped Conversation's resolveNamespace), so gateways passing namespace
// via context must ensure the StreamSession field is set correctly.
type StreamSession struct {
	conv      *Conversation
	Namespace string
}

// NewStreamSession builds a StreamSession.
func NewStreamSession(conv *Conversation, namespace string) *StreamSession {
	return &StreamSession{conv: conv, Namespace: namespace}
}

// Execute streams the assistant response for a user turn.
// Handles tool calls transparently by executing them and continuing
// the stream.
//
// Implements TurnExecutor by delegating to the shared Conversation
// lifecycle with a streaming step function. Session setup (get-or-create,
// append user message) uses StreamSession.Namespace directly, while the
// tool loop and event emission use the shared runTurnWithStep.
func (ss *StreamSession) Execute(ctx context.Context, sessionID, userInput string) (<-chan event.EngineEvent, error) {
	session, err := ss.conv.Sessions.GetOrCreate(ctx, ss.Namespace, sessionID)
	if err != nil {
		return nil, err
	}
	session.Append(Message{Role: RoleUser, Content: userInput})
	if err := ss.conv.Sessions.Save(ctx, ss.Namespace, session); err != nil {
		return nil, err
	}
	// Promote namespace into context if not already set (e.g. by Telegram
	// gateway), so that runTurnWithStep's internal session persistence
	// (finishTurn, autoRenameIfNeeded) uses the correct namespace.
	if event.NamespaceFromContext(ctx) == "" {
		ctx = event.ContextWithNamespace(ctx, ss.Namespace)
	}
	return ss.conv.runTurnWithStep(ctx, session, ss.streamStep(session)), nil
}

// streamStep creates a step function that uses adapter.Stream to obtain
// LLM responses incrementally, emitting TextDelta events for each chunk.
//
// The streaming loop transparently handles tool-call fallback: when the
// model requests tools, the accumulated tool calls are returned so the
// engine can execute them and iterate again.
func (ss *StreamSession) streamStep(session SessionView) stepFunc {
	return func(ctx context.Context, msgs []Message, schemas []ToolSchema, events eventSender) (*llmStepResult, error) {
		resultCh, err := ss.conv.adapterFor(session).Stream(ctx, msgs, schemas, ss.conv.Config.Options)
		if err != nil {
			return nil, err
		}

		var textBuf strings.Builder
		var toolCalls []ToolCall
		var usage *Usage

		for result := range resultCh {
			switch {
			case result.Err != nil:
				return nil, result.Err

			case result.Delta != "":
				textBuf.WriteString(result.Delta)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case events <- event.TextDelta{SessionID: session.SessionID(), Delta: result.Delta}:
				}

			case len(result.ToolCalls) > 0:
				toolCalls = append(toolCalls, result.ToolCalls...)
				usage = result.Usage

			case result.Done:
				usage = result.Usage
			}
		}

		return &llmStepResult{
			content:   textBuf.String(),
			toolCalls: toolCalls,
			usage:     usage,
		}, nil
	}
}
