package commands

import (
	"context"
	"encoding/json"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ModelCommands handles model-related commands.
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
	if mc.Conv == nil || mc.Conv.Router() == nil {
		return CommandResult{Handled: true, Cmd: "model_list", Reply: "no router configured", Session: session}
	}

	current := mc.modelName(session)
	names := mc.Conv.Router().Models()
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
