package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent/event"
)

// copyBackStore is a session store that returns deep copies on Get and
// stores a deep copy on Put. This simulates SQLiteStore behavior where
// each Get returns a fresh object — the in-memory Session pointer held
// by the turn is NEVER the same as the one returned to tool handlers.
type copyBackStore struct {
	mu   sync.RWMutex
	data map[string]SessionData // key = "namespace:id"
}

func newCopyBackStore() *copyBackStore {
	return &copyBackStore{data: make(map[string]SessionData)}
}

func (c *copyBackStore) key(namespace, id string) string { return namespace + ":" + id }

func (c *copyBackStore) Get(_ context.Context, namespace, id string) (*Session, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.data[c.key(namespace, id)]
	if !ok {
		return nil, false, nil
	}
	cp := d.DeepCopy()
	return NewSessionFromData(&cp), true, nil
}

func (c *copyBackStore) Put(_ context.Context, namespace string, s *Session) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data := s.Read()
	c.data[c.key(namespace, data.ID)] = data
	return nil
}

func (c *copyBackStore) Delete(_ context.Context, namespace, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, c.key(namespace, id))
	return nil
}

func (c *copyBackStore) List(_ context.Context, namespace string) ([]*Session, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*Session
	for key, d := range c.data {
		if len(key) > len(namespace) && key[:len(namespace)] == namespace && key[len(namespace)] == ':' {
			cp := d.DeepCopy()
			out = append(out, NewSessionFromData(&cp))
		}
	}
	return out, nil
}

// TestMidTurnToolEditor_ContextInjection verifies that a tool handler
// can enable a tool via the context-injected session view, and the
// engine sees the change in the next iteration without any reloadFn.
//
// This test uses a deep-copy store (copyBackStore) which would have
// exposed the old stale-session bug. With context injection, the tool
// handler mutates the engine's session in-place, so no reload is needed.
func TestMidTurnToolEditor_ContextInjection(t *testing.T) {
	store := newCopyBackStore()
	sm := NewSessionManager(store, "sys")
	session, err := sm.GetOrCreate(context.Background(), "testns", "")
	if err != nil {
		t.Fatal(err)
	}

	tools := NewToolRegistry()
	var testToolExecuted bool
	tools.Register(Tool{
		Schema: ToolSchema{Name: "test_tool"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			testToolExecuted = true
			return "test_tool executed!", nil
		},
	})
	tools.Register(Tool{
		Schema: ToolSchema{Name: "inline_enable"},
		Tags:   []string{"tool"},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			// Use context-injected session — this is the NEW approach.
			// The engine injects the session before every tool invocation.
			sess, ok := event.SessionViewFromContext(ctx).(SessionToolEditor)
			if !ok || sess == nil {
				return "", fmt.Errorf("inline_enable: no session in context")
			}
			sess.EnableTool(args.Name)
			return "enabled " + args.Name, nil
		},
	})

	rr := newTurnRunner(tools, newToolExecutor(tools, nil, Hooks{}), EngineConfig{
		MaxToolIterations: 4,
		Logger:            testLogger(t),
	}, &Router{}, testLogger(t))

	callCount := 0
	eventCh := make(chan event.EngineEvent, 256)
	step := func(ctx context.Context, msgs []Message, schemas []ToolSchema, ev eventSender) (*llmStepResult, error) {
		callCount++
		switch callCount {
		case 1:
			// First call: schemas should NOT include test_tool (not enabled yet)
			hasTestTool := false
			for _, s := range schemas {
				if s.Name == "test_tool" {
					hasTestTool = true
					break
				}
			}
			if hasTestTool {
				t.Error("test_tool should NOT be in schemas before enable_tool")
			}
			return &llmStepResult{
				toolCalls: []ToolCall{{
					ID: "call-1", Name: "inline_enable", Arguments: `{"name":"test_tool"}`,
				}},
			}, nil
		case 2:
			// Second call: schemas SHOULD include test_tool (enabled by context injection)
			hasTestTool := false
			for _, s := range schemas {
				if s.Name == "test_tool" {
					hasTestTool = true
					break
				}
			}
			if !hasTestTool {
				t.Error("test_tool SHOULD be in schemas — context injection should have enabled it")
			}
			return &llmStepResult{
				toolCalls: []ToolCall{{
					ID: "call-2", Name: "test_tool", Arguments: `{}`,
				}},
			}, nil
		default:
			return &llmStepResult{content: "done"}, nil
		}
	}

	saveFn := func(ctx context.Context, s SessionView) error {
		return sm.Save(ctx, "testns", s)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reply, err := rr.run(ctx, session, eventCh, step, saveFn)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if reply != "done" {
		t.Fatalf("unexpected reply: %q", reply)
	}
	if callCount != 3 {
		t.Fatalf("expected 3 LLM calls (inline_enable + test_tool + done), got %d", callCount)
	}
	if !testToolExecuted {
		t.Fatal("test_tool was NOT executed — context injection should have enabled it")
	}
}

// TestContextEstimatedStoredOnSession verifies that the turn runner stores
// the estimated context token count on the session before each LLM call.
func TestContextEstimatedStoredOnSession(t *testing.T) {
	store := newCopyBackStore()
	sm := NewSessionManager(store, "sys")
	session, err := sm.GetOrCreate(context.Background(), "testns", "")
	if err != nil {
		t.Fatal(err)
	}

	// Add some messages to give the heuristic estimate something to count
	session.Append(Message{Role: RoleUser, Content: "What is the meaning of life, the universe, and everything?"})
	session.Append(Message{Role: RoleAssistant, Content: "42. It is the answer to the ultimate question. Deep thought computed it over millions of years."})

	tools := NewToolRegistry()

	rr := newTurnRunner(tools, newToolExecutor(tools, nil, Hooks{}), EngineConfig{
		MaxToolIterations: 3,
		Logger:            testLogger(t),
	}, &Router{}, testLogger(t))

	eventCh := make(chan event.EngineEvent, 64)

	step := func(ctx context.Context, msgs []Message, schemas []ToolSchema, ev eventSender) (*llmStepResult, error) {
		return &llmStepResult{content: "hello"}, nil
	}

	saveFn := func(ctx context.Context, s SessionView) error {
		return sm.Save(ctx, "testns", s)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reply, err := rr.run(ctx, session, eventCh, step, saveFn)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if reply != "hello" {
		t.Fatalf("unexpected reply: %q", reply)
	}

	// The session should have the estimated context tokens stored
	estimated := session.GetEstimatedContextTokens()
	if estimated <= 0 {
		t.Fatalf("expected positive estimated context tokens, got %d", estimated)
	}
}
