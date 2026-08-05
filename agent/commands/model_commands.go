package commands

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

type ModelCommands struct {
	Sessions *agent.SessionManager
	Conv     *agent.Conversation
	NS       string
}

func NewModelCommands(sm *agent.SessionManager, conv *agent.Conversation, ns string, reg *CommandRegistry) *ModelCommands {
	mc := &ModelCommands{Sessions: sm, Conv: conv, NS: ns}

	reg.Register(Command{
		Name: "model_list", Description: "List available models", Display: "model list",
		Handler: mc.jsonModelList,
	})
	reg.Register(Command{
		Name: "model_switch", Description: "Switch to a different model", Display: "model switch",
		Params:  map[string]string{"name": "model name"},
		Handler: mc.jsonModelSwitch,
	})

	return mc
}

func (mc *ModelCommands) resolveNamespace(ctx context.Context) string {
	return event.NamespaceFromContext(ctx)
}

func (mc *ModelCommands) jsonModelList(ctx context.Context, sessionID string, params json.RawMessage) CommandResult {
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

	result := map[string]any{"model": p.Name}

	// Fetch quota for the newly selected model.
	if mc.Conv != nil && mc.Conv.Router() != nil {
		adapter := mc.Conv.Router().Adapter(session)
		if qf, ok := adapter.(agent.QuotaFetcher); ok {
			fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if info, err := qf.FetchQuota(fetchCtx); err == nil && info != nil {
				if info.Balance != nil {
					result["quota_balance"] = *info.Balance
				}
				if info.Currency != "" {
					result["quota_currency"] = info.Currency
				}
			}
		}
	}

	data, _ := json.Marshal(result)
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
