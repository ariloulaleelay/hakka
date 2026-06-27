// Package batch provides a reusable API for running Hakka in batch mode —
// a single autonomous task that runs to completion without any interactive
// transport (TCP, WebSocket, or Telegram).
//
// Usage:
//
//	p := batch.RunBatchParams{
//	    Registry: myRegistry,
//	    Task:     "Write a test for ...",
//	}
//	reply, err := batch.RunBatch(ctx, p)
package batch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
	hakkatools "github.com/ariloulaleelay/hakka/agent/tools"
)

// RunBatchParams holds parameters for a batch run.
type RunBatchParams struct {
	Registry         *agent.Registry
	Task             string
	Tools            *agent.ToolRegistry // optional; defaults to all built-in tools
	EnableTools      []string            // tool names or tags to enable; nil/empty means no tools enabled
	CompactSoftLimit int                 // 0 = use engine config default
	Logger           *slog.Logger        // optional; defaults to slog.Default()
	Store            agent.SessionStore  // optional; defaults to in-memory store
}

// ResolveToolsByTagOrName resolves a list of tool/tag references to a
// deduplicated list of tool names.
//
// References starting with "#" are treated exclusively as tags — the #
// prefix is stripped and all tools with that tag are included.
// Plain references (no "#") are first tried as exact tool names; if no
// match is found, they are treated as a tag and all tools with that tag
// are included.
func ResolveToolsByTagOrName(tools *agent.ToolRegistry, refs []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if strings.HasPrefix(ref, "#") {
			// Tag-prefixed reference: strip "#" and treat as tag only.
			tag := strings.TrimSpace(ref[1:])
			if tag == "" {
				continue
			}
			for _, s := range tools.SchemasByTags(tag) {
				if !seen[s.Name] {
					seen[s.Name] = true
					result = append(result, s.Name)
				}
			}
			continue
		}
		// Plain reference: try exact name first.
		if _, ok := tools.Get(ref); ok {
			if !seen[ref] {
				seen[ref] = true
				result = append(result, ref)
			}
			continue
		}
		// Not a tool name — treat as tag.
		for _, s := range tools.SchemasByTags(ref) {
			if !seen[s.Name] {
				seen[s.Name] = true
				result = append(result, s.Name)
			}
		}
	}
	return result
}

// RunBatch runs a single autonomous task in batch mode and returns the
// final assistant reply. All output goes to stdout (reply) and stderr
// (session ID + token info).
func RunBatch(ctx context.Context, p RunBatchParams) (string, error) {
	return RunBatchWithOutput(ctx, p, os.Stdout, os.Stderr)
}

// RunBatchWithOutput is like RunBatch but allows capturing stdout and
// stderr-like output to the given writers.
func RunBatchWithOutput(ctx context.Context, p RunBatchParams, output io.Writer, aux io.Writer) (string, error) {
	reg := p.Registry
	if reg == nil {
		return "", errors.New("registry is required")
	}
	if p.Task == "" {
		return "", errors.New("task is required")
	}
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Use in-memory store — batch runs are ephemeral.
	store := p.Store
	if store == nil {
		store = agent.NewMemoryStore()
	}
	sessions := agent.NewSessionManager(store, "You are Hakka, a helpful assistant.")
	router := agent.NewRouter(reg)

	tools := p.Tools
	if tools == nil {
		tools = agent.NewToolRegistry()
		pm := hakkatools.NewProcessManager()
		hakkatools.RegisterAll(tools)
		hakkatools.RegisterMeta(tools)
		hakkatools.RegisterProcessTools(tools, pm)
		hakkatools.RegisterSessionTools(tools, sessions, nil)
	}

	cfg := agent.DefaultEngineConfig()
	cfg.Logger = logger

	conv := agent.NewConversation(sessions, router, tools, "batch", cfg)

	// Resolve tool names/tags and enable them on the session.
	resolved := ResolveToolsByTagOrName(tools, p.EnableTools)
	session, err := sessions.GetOrCreate(ctx, "batch", "")
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	for _, name := range resolved {
		session.EnableTool(name)
	}
	if p.CompactSoftLimit > 0 {
		session.SetCompactSoftLimit(p.CompactSoftLimit)
	}
	if err := sessions.Save(ctx, "batch", session); err != nil {
		return "", fmt.Errorf("save session: %w", err)
	}

	eventCh, err := conv.Execute(ctx, session.SessionID(), p.Task)
	if err != nil {
		return "", err
	}

	var reply string
	for evt := range eventCh {
		if tf, ok := evt.(event.TurnFinished); ok {
			if tf.Err != nil {
				return "", tf.Err
			}
			reply = tf.Reply
			if output != nil {
				fmt.Fprintln(output, reply)
			}
			if aux != nil {
				fmt.Fprintf(aux, "session: %s\n", tf.SessionID)
				fmt.Fprintf(aux, "tokens: %d\n", tf.TotalTokens)
			}
		}
	}
	return reply, nil
}
