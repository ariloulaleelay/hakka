package commands

import (
	"context"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// ModelCommands handles /model and /models slash commands.
// ---------------------------------------------------------------------------

// ModelCommands handles model-related slash commands.
// Namespace is resolved from context at each operation, so the same
// handler can serve multiple isolated namespaces concurrently.
type ModelCommands struct {
	Sessions *agent.SessionManager
	Conv     *agent.Conversation // used for model binding; can be nil
	NS       string              // fallback namespace (used when context has none)
}

// NewModelCommands builds a ModelCommands handler.
func NewModelCommands(sm *agent.SessionManager, conv *agent.Conversation, ns string) *ModelCommands {
	return &ModelCommands{Sessions: sm, Conv: conv, NS: ns}
}

// resolveNamespace returns the namespace from context.
func (mc *ModelCommands) resolveNamespace(ctx context.Context) string {
	return event.NamespaceFromContext(ctx)
}

// Handle dispatches to the appropriate sub-handler based on parts.
// Parts[0] is "/model" or "/models".
func (mc *ModelCommands) Handle(ctx context.Context, sessionID string, parts []string, cmd string) CommandResult {
	switch cmd {
	case "/models":
		return mc.handleModelsList(ctx, sessionID)
	case "/model":
		return mc.handleModel(ctx, sessionID, parts)
	default:
		return CommandResult{Handled: false}
	}
}

func (mc *ModelCommands) handleModel(ctx context.Context, sessionID string, parts []string) CommandResult {
	if len(parts) >= 2 {
		sub := parts[1]
		switch sub {
		case "list":
			return mc.handleModelsList(ctx, sessionID)
		case "show":
			return mc.showCurrentModel(ctx, sessionID)
		case "switch":
			if len(parts) < 3 {
				return CommandResult{Handled: true, Action: ActionReply, Reply: "error: please specify a model name to switch to"}
			}
			return mc.setAndReportModel(ctx, sessionID, parts[2])
		}
	}

	if len(parts) < 2 {
		return mc.showCurrentModel(ctx, sessionID)
	}
	return mc.setAndReportModel(ctx, sessionID, parts[1])
}

func (mc *ModelCommands) showCurrentModel(ctx context.Context, sessionID string) CommandResult {
	ns := mc.resolveNamespace(ctx)
	session, err := mc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: "current model: " + mc.modelName(session), Session: session}
}

func (mc *ModelCommands) setAndReportModel(ctx context.Context, sessionID, name string) CommandResult {
	session, err := mc.bindModel(ctx, sessionID, name)
	if err != nil {
		return CommandResult{Handled: true, Session: session, Error: err}
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: "model set to: " + name, Session: session}
}

func (mc *ModelCommands) handleModelsList(ctx context.Context, sessionID string) CommandResult {
	ns := mc.resolveNamespace(ctx)
	session, err := mc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Error: err}
	}
	if mc.Conv == nil || mc.Conv.Router == nil {
		return CommandResult{Handled: true, Action: ActionReply, Reply: "no router configured", Session: session}
	}
	current := mc.modelName(session)
	names := mc.Conv.Router.Models()
	lines := make([]string, 0, len(names))
	for _, n := range names {
		mark := "  "
		if n == current {
			mark = "* "
		}
		lines = append(lines, mark+n)
	}
	return CommandResult{Handled: true, Action: ActionReply, Reply: "available models:\n" + strings.Join(lines, "\n"), Session: session}
}

func (mc *ModelCommands) modelName(session *agent.Session) string {
	if mc.Conv != nil {
		return mc.Conv.SessionModel(session)
	}
	return session.GetModel()
}

func (mc *ModelCommands) bindModel(ctx context.Context, sessionID, name string) (*agent.Session, error) {
	if mc.Conv != nil {
		return mc.Conv.BindSessionModel(ctx, sessionID, name)
	}
	ns := mc.resolveNamespace(ctx)
	session, err := mc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return nil, err
	}
	session.SetModel(name)
	if err := mc.Sessions.Save(ctx, ns, session); err != nil {
		return session, err
	}
	return session, nil
}
