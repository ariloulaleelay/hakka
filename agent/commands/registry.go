package commands

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
)

// CommandHandler is the function signature all registered commands must
// satisfy. It receives a context, the current session ID, and raw JSON
// parameters; it returns a CommandResult describing the outcome.
//
// The context is guaranteed to have a namespace set by the time the
// handler is called (CommandRegistry.Execute ensures this).
type CommandHandler func(ctx context.Context, sessionID string, params json.RawMessage) CommandResult

// Command is a registered command with its name, description, optional
// display text, optional parameter descriptions, and handler.
type Command struct {
	Name        string
	Description string
	Display     string            // optional; shown in help menu instead of Name when set
	Params      map[string]string // optional; parameter-name → type/description
	Handler     CommandHandler
}

// CommandRegistry is a goroutine-safe collection of commands.
// Commands are registered at startup and dispatched by name at runtime.
type CommandRegistry struct {
	mu       sync.RWMutex
	commands map[string]Command
}

// NewCommandRegistry returns an empty, ready-to-use command registry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{commands: make(map[string]Command)}
}

// Register adds a command to the registry. If a command with the same
// name already exists, it is replaced silently.
func (r *CommandRegistry) Register(cmd Command) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands[cmd.Name] = cmd
}

// Execute looks up the named command and invokes its handler. Returns
// (result, true) when the command was found, or (zero, false) when
// no command with that name is registered.
func (r *CommandRegistry) Execute(ctx context.Context, sessionID, cmd string, params json.RawMessage) (CommandResult, bool) {
	r.mu.RLock()
	registered, ok := r.commands[cmd]
	r.mu.RUnlock()
	if !ok {
		return CommandResult{}, false
	}
	result := registered.Handler(ctx, sessionID, params)
	result.Cmd = cmd
	result.Handled = true
	return result, true
}

// List returns all registered commands sorted alphabetically by name.
func (r *CommandRegistry) List() []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Command, 0, len(r.commands))
	for _, c := range r.commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
