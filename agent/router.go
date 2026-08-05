package agent

import (
	"errors"
	"fmt"
	"log/slog"
)

// Router decides which LLMAdapter handles a given session. It is the
// single owner of the session's model binding; no other code should
// read or write the Model field directly.
//
// Router depends on SessionModelBinding (not the concrete Session type)
// to decouple routing policy from session data structures, following the
// Dependency Inversion Principle and the Interface Segregation Principle.
type Router struct {
	models *Registry
}

// NewRouter builds a Router backed by the given Registry. A nil registry
// is not valid; callers wiring a single adapter should construct a
// Registry with one entry.
func NewRouter(models *Registry) *Router {
	return &Router{models: models}
}

// Adapter returns the LLMAdapter for the session. When the session has
// no recorded model, the registry default is used. When the session's
// recorded model is no longer registered, nil is returned — the caller
// must surface this as an error so the user can switch to a valid model.
func (router *Router) Adapter(session SessionModelBinding) LLMAdapter {
	if router == nil || router.models == nil {
		return nil
	}
	if name := router.current(session); name != "" {
		if a, ok := router.models.Get(name); ok {
			return a
		}
		slog.Error("stored model not found in registry — user must switch models",
			"model", name, "available", router.models.Names())
		return nil
	}
	a, _ := router.models.Get(router.models.Default())
	return a
}

// Current returns the model name, or the registry default if none is bound.
// Returns "" only when there is no registry.
func (router *Router) Current(session SessionModelBinding) string {
	if router == nil || router.models == nil {
		return ""
	}
	if name := router.current(session); name != "" {
		return name
	}
	return router.models.Default()
}

// Bind records a model choice on the session. Does not persist — callers must
// Save the session separately.
func (router *Router) Bind(session SessionModelBinding, name string) error {
	if router == nil || router.models == nil {
		return errors.New("router: no registry configured")
	}
	if _, ok := router.models.Get(name); !ok {
		return fmt.Errorf("router: unknown model %q", name)
	}
	session.SetModel(name)
	return nil
}

func (router *Router) Models() []string {
	if router == nil || router.models == nil {
		return nil
	}
	return router.models.Names()
}

// Registry exposes the underlying registry. It exists so tests and
// composition code can inspect/configure the adapter set; production
// code should prefer Adapter, Current, Bind, Models.
func (router *Router) Registry() *Registry {
	if router == nil {
		return nil
	}
	return router.models
}

// GetProfile returns the ModelProfile for the session's current model,
// or false if the model is unknown.
func (router *Router) GetProfile(session SessionModelBinding) (ModelProfile, bool) {
	name := router.Current(session)
	if name == "" {
		return ModelProfile{}, false
	}
	return router.models.GetProfile(name)
}

// current reads the recorded model name without falling back.
func (router *Router) current(session SessionModelBinding) string {
	if session == nil {
		return ""
	}
	return session.GetModel()
}
