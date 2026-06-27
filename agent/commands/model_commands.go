package commands

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ModelCommands handles model-related slash commands.
type ModelCommands struct {
	Sessions *agent.SessionManager
	Conv     *agent.Conversation
	NS       string
}

func NewModelCommands(sm *agent.SessionManager, conv *agent.Conversation, ns string) *ModelCommands {
	return &ModelCommands{Sessions: sm, Conv: conv, NS: ns}
}

func (mc *ModelCommands) resolveNamespace(ctx context.Context) string {
	return event.NamespaceFromContext(ctx)
}

// HandleJSON handles a structured JSON model command.
func (mc *ModelCommands) HandleJSON(ctx context.Context, sessionID, cmd string, params json.RawMessage) CommandResult {
	switch cmd {
	case "model_list":
		return mc.jsonModelList(ctx, sessionID)
	case "model_switch":
		return mc.jsonModelSwitch(ctx, sessionID, params)
	}
	return CommandResult{Handled: false}
}

func (mc *ModelCommands) jsonModelList(ctx context.Context, sessionID string) CommandResult {
	ns := mc.resolveNamespace(ctx)
	session, err := mc.Sessions.GetOrCreate(ctx, ns, sessionID)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "model_list", Error: err}
	}
	if mc.Conv == nil || mc.Conv.Router == nil {
		return CommandResult{Handled: true, Cmd: "model_list", Reply: "no router configured", Session: session}
	}

	current := mc.modelName(session)
	names := mc.Conv.Router.Models()
	models := make([]map[string]any, 0, len(names))
	for _, n := range names {
		models = append(models, map[string]any{
			"name":    n,
			"current": n == current,
		})
	}

	data, _ := json.Marshal(map[string]any{"models": models})
	return CommandResult{Handled: true, Cmd: "model_list", Data: data, Session: session}
}

func (mc *ModelCommands) jsonModelSwitch(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
	var p struct {
		Name string `json:"name"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}
	if p.Name == "" {
		return CommandResult{Handled: true, Cmd: "model_switch", Reply: "error: please specify a model name"}
	}

	session, err := mc.bindModel(ctx, sessionID, p.Name)
	if err != nil {
		return CommandResult{Handled: true, Cmd: "model_switch", Session: session, Error: err}
	}

	data, _ := json.Marshal(map[string]any{"model": p.Name})
	return CommandResult{Handled: true, Cmd: "model_switch", Data: data, Session: session}
}

// --- Text-based handlers ---

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
