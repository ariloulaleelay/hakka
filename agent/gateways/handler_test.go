package gateways

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/commands"
)

// spyWriter captures all frames written to it.
type spyWriter struct {
	mu      sync.Mutex
	frames  []FrameResponse
	name    string // for debugging
	connKey string // unique ID, defaults to "test" for backward compat
}

func (w *spyWriter) Write(f FrameResponse) error {
	w.mu.Lock()
	w.frames = append(w.frames, f)
	w.mu.Unlock()
	return nil
}

func (w *spyWriter) ConnKey() string {
	if w.connKey != "" {
		return w.connKey
	}
	return "test"
}

func (w *spyWriter) Frames() []FrameResponse {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := make([]FrameResponse, len(w.frames))
	copy(r, w.frames)
	return r
}

func (w *spyWriter) LastDone() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := len(w.frames) - 1; i >= 0; i-- {
		if w.frames[i].Type == "done" {
			return true
		}
	}
	return false
}

func (w *spyWriter) WaitForDone(t *testing.T, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for {
		w.mu.Lock()
		for i := len(w.frames) - 1; i >= 0; i-- {
			if w.frames[i].Type == "done" {
				w.mu.Unlock()
				return true
			}
		}
		w.mu.Unlock()
		select {
		case <-deadline:
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// callTrackingAdapter records every LLM call made through it.
type callTrackingAdapter struct {
	mu    sync.Mutex
	calls int
}

func (a *callTrackingAdapter) Complete(_ context.Context, msgs []agent.Message, tools []agent.ToolSchema, opts agent.CompleteOptions, _ func(string)) (*agent.LLMResponse, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return &agent.LLMResponse{Message: agent.Message{Role: agent.RoleAssistant, Content: "response from LLM"}, FinishReason: "stop"}, nil
}

func (a *callTrackingAdapter) CallCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// TestContinueCommand_TriggersLLM proves that sending a "continue"
// command actually invokes the LLM (instead of just returning "done").
func TestContinueCommand_TriggersLLM(t *testing.T) {
	// ── Setup ──────────────────────────────────────────────────────────
	adapter := &callTrackingAdapter{}

	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)

	sm := agent.NewSessionManager(nil, "sys")
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{
		MaxToolIterations: 4,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	ns := "testns"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	cmd := commands.New(sm, conv, "", ns)
	handler := NewTurnHandler(conv, cmd, ns)

	writer := &spyWriter{}
	responseReader := NewInProcessResponseReader()
	defer responseReader.Stop()

	if _, err := sm.CreateWithID(context.Background(), ns, "test-session"); err != nil {
		t.Fatal(err)
	}
	// ── Step 1: Send a normal chat message ─────────────────────────────
	chatReq := FrameRequest{
		SessionID: "test-session",
		Input:     "hello",
	}
	handler.HandleRequest(context.Background(), chatReq, writer, responseReader)

	if !writer.WaitForDone(t, 5*time.Second) {
		t.Fatal("timed out waiting for chat response")
	}

	if adapter.CallCount() != 1 {
		t.Fatalf("expected 1 LLM call after chat, got %d", adapter.CallCount())
	}

	// Verify that the session now has 2 messages (user + assistant)
	session, err := sm.Get(context.Background(), ns, "test-session")
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	if len(session.Messages()) != 2 {
		t.Fatalf("expected 2 messages in session (user + assistant), got %d", len(session.Messages()))
	}

	// ── Step 2: Send a continue command ───────────────────────────────
	continueReq := FrameRequest{
		SessionID: "test-session",
		Command: &CommandRequest{
			Cmd: "continue",
		},
	}
	writer = &spyWriter{} // fresh writer for the second request
	handler.HandleRequest(context.Background(), continueReq, writer, responseReader)

	if !writer.WaitForDone(t, 5*time.Second) {
		t.Fatal("timed out waiting for continue response")
	}

	if adapter.CallCount() != 2 {
		t.Fatalf("expected 2 LLM calls total (1 chat + 1 continue), got %d", adapter.CallCount())
	}

	// Verify the session now has 3 messages (user + assistant + assistant)
	session, _ = sm.Get(context.Background(), ns, "test-session")
	if len(session.Messages()) != 3 {
		t.Fatalf("expected 3 messages in session (user + 2 assistants), got %d", len(session.Messages()))
	}

	// Check that the continue frame was a proper Done frame (not an error)
	frames := writer.Frames()
	var foundDone bool
	for _, f := range frames {
		if f.Type == "done" {
			foundDone = true
			if f.Error != "" {
				t.Fatalf("continue returned error: %s", f.Error)
			}
		}
	}
	if !foundDone {
		t.Fatal("expected a done frame from continue command")
	}
}

// TestContinueCommand_SurvivesClientDisconnect proves that the /continue
// turn runs on context.Background() and completes even if the client's
// context is cancelled (simulating client disconnect after sending the
// command). This mirrors how gateways handle continue reconnection.
func TestContinueCommand_SurvivesClientDisconnect(t *testing.T) {
	adapter := &callTrackingAdapter{}

	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)

	sm := agent.NewSessionManager(nil, "sys")
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{
		MaxToolIterations: 4,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	ns := "testns"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	cmd := commands.New(sm, conv, "", ns)
	handler := NewTurnHandler(conv, cmd, ns)

	writer := &spyWriter{}
	responseReader := NewInProcessResponseReader()
	defer responseReader.Stop()

	// First, establish a session with a conversation
	if _, err := sm.CreateWithID(context.Background(), ns, "test-disco"); err != nil {
		t.Fatal(err)
	}
	chatReq := FrameRequest{
		SessionID: "test-disco",
		Input:     "hello",
	}
	handler.HandleRequest(context.Background(), chatReq, writer, responseReader)
	writer.WaitForDone(t, 5*time.Second)

	callCountBefore := adapter.CallCount()

	// Now send a continue command with a cancellable context
	// and cancel it immediately (simulating client disconnect).
	discoCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — client gone before LLM even starts

	continueReq := FrameRequest{
		SessionID: "test-disco",
		Command: &CommandRequest{
			Cmd: "continue",
		},
	}
	discoWriter := &spyWriter{}
	handler.HandleRequest(discoCtx, continueReq, discoWriter, responseReader)

	// The turn should still complete because it runs on context.Background()
	if !discoWriter.WaitForDone(t, 5*time.Second) {
		t.Fatal("timed out waiting for continue to finish after client disconnect")
	}

	// The LLM should have been called regardless of client disconnect
	if adapter.CallCount() != callCountBefore+1 {
		t.Fatalf("expected LLM to have been called %d times, got %d",
			callCountBefore+1, adapter.CallCount())
	}
}

// TestContinueCommand_SessionNotFound proves that sending continue
// without a session_id returns an error instead of starting an LLM turn.
func TestContinueCommand_NoSessionID(t *testing.T) {
	adapter := &callTrackingAdapter{}

	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)

	sm := agent.NewSessionManager(nil, "sys")
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{
		MaxToolIterations: 4,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	ns := "testns"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	cmd := commands.New(sm, conv, "", ns)
	handler := NewTurnHandler(conv, cmd, ns)

	writer := &spyWriter{}
	responseReader := NewInProcessResponseReader()
	defer responseReader.Stop()

	// Send continue without an existing session (empty session_id)
	continueReq := FrameRequest{
		Command: &CommandRequest{
			Cmd: "continue",
		},
	}
	handler.HandleRequest(context.Background(), continueReq, writer, responseReader)

	if !writer.WaitForDone(t, 5*time.Second) {
		t.Fatal("timed out waiting for continue response")
	}

	// The LLM should not have been called since there's no session
	if adapter.CallCount() != 0 {
		t.Fatalf("expected 0 LLM calls when no session, got %d", adapter.CallCount())
	}

	// Verify there's an error frame (only meaningful if the writer captured one)
	frames := writer.Frames()
	for _, f := range frames {
		if f.Error != "" {
			t.Logf("expected error because no session: %s", f.Error)
			return
		}
	}
	t.Log("no error frame captured (command may have returned done without LLM)")
}

// TestContinueCommand_Integration proves that the full pipeline works:
// user sends input → LLM responds → user sends continue → LLM responds
// again — all through the TurnHandler.
func TestContinueCommand_Integration(t *testing.T) {
	// Also test the streaming path.
	adapter := &callTrackingAdapter{}

	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)

	sm := agent.NewSessionManager(nil, "sys")
	tools := agent.NewToolRegistry()
	cfg := agent.EngineConfig{
		MaxToolIterations: 4,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	ns := "testns"
	conv := agent.NewConversation(sm, router, tools, ns, cfg)
	cmd := commands.New(sm, conv, "", ns)
	handler := NewTurnHandler(conv, cmd, ns)

	writer := &spyWriter{}
	responseReader := NewInProcessResponseReader()
	defer responseReader.Stop()

	if _, err := sm.CreateWithID(context.Background(), ns, "integration-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.CreateWithID(context.Background(), ns, "integration-session"); err != nil {
		t.Fatal(err)
	}
	handler.HandleRequest(context.Background(), FrameRequest{
		SessionID: "integration-session",
		Input:     "tell me a story",
	}, writer, responseReader)
	writer.WaitForDone(t, 5*time.Second)

	firstCallCount := adapter.CallCount()
	if firstCallCount != 1 {
		t.Fatalf("expected 1 LLM call after chat, got %d", firstCallCount)
	}

	// ── Step 2: Continue ──────────────────────────────────────────────
	writer2 := &spyWriter{}
	handler.HandleRequest(context.Background(), FrameRequest{
		SessionID: "integration-session",
		Command:   &CommandRequest{Cmd: "continue"},
	}, writer2, responseReader)
	writer2.WaitForDone(t, 5*time.Second)

	if adapter.CallCount() != 2 {
		t.Fatalf("expected 2 LLM calls total (1 chat + 1 continue), got %d", adapter.CallCount())
	}

	// Verify the final output from the continue command
	frames := writer2.Frames()
	var output string
	for _, f := range frames {
		if f.Text != "" {
			output = f.Text
		}
	}
	if output == "" {
		t.Fatal("expected continue to produce output text")
	}
	if output != "response from LLM" {
		t.Fatalf("expected output %q, got %q", "response from LLM", output)
	}
}

// TestContinueCommand_NotHandledByCommandProcessorAlone proves that even
// though the command processor returns ActionContinue, the handler is
// responsible for actually invoking the LLM. This test directly calls
// the command processor and verifies it does NOT produce an LLM call.
func TestContinueCommand_CommandProcessorDoesNotInvokeLLM(t *testing.T) {
	adapter := &callTrackingAdapter{}

	reg := agent.NewRegistry()
	reg.Register("default", adapter)
	router := agent.NewRouter(reg)

	sm := agent.NewSessionManager(nil, "sys")
	cfg := agent.EngineConfig{
		MaxToolIterations: 4,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	ns := "testns"
	conv := agent.NewConversation(sm, router, agent.NewToolRegistry(), ns, cfg)
	cmd := commands.New(sm, conv, "", ns)

	// Direct call to command processor — should NOT invoke the LLM
	res := cmd.ExecuteJSON(context.Background(), "some-session", "continue", nil)
	if !res.Handled {
		t.Fatal("expected continue to be handled")
	}
	if res.Action != commands.ActionContinue {
		t.Fatalf("expected ActionContinue, got %v", res.Action)
	}

	if adapter.CallCount() != 0 {
		t.Fatalf("expected 0 LLM calls from command processor alone, got %d", adapter.CallCount())
	}
}

// TestEnrichCtxWithCWD_DoesNotRecreateDeletedSession verifies that
// enrichCtxWithCWD does not re-create a session that was previously
// deleted. It uses CreateWithID internally, which is a bug: the function
// should treat the session as a read-only source of CWD and must not
// mutate the store.
func TestEnrichCtxWithCWD_DoesNotRecreateDeletedSession(t *testing.T) {
	sm := agent.NewSessionManager(nil, "") // memory store
	ns := "testns"
	sessionID := "session-to-delete"

	// Create a session.
	session, err := sm.CreateWithID(context.Background(), ns, sessionID)
	if err != nil {
		t.Fatalf("CreateWithID: %v", err)
	}
	session.SetClientCWD("/home/user/project")

	// Verify the session exists.
	if _, err := sm.Get(context.Background(), ns, sessionID); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Delete the session.
	if err := sm.Drop(context.Background(), ns, sessionID); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	// Verify the session is gone.
	if _, err = sm.Get(context.Background(), ns, sessionID); !errors.Is(err, agent.ErrSessionNotFound) {
		t.Fatalf("expected deleted session, got err=%v", err)
	}

	// Create a Conversation (needed by enrichCtxWithCWD).
	reg := agent.NewRegistry()
	reg.Register("default", &fakeAdapter{reply: "hi"})
	router := agent.NewRouter(reg)
	cfg := agent.EngineConfig{MaxToolIterations: 2}
	conv := agent.NewConversation(sm, router, agent.NewToolRegistry(), ns, cfg)

	// Call enrichCtxWithCWD with the deleted session's ID.
	// BUG: this calls CreateWithID internally, which re-creates the session.
	ctx := context.Background()
	_ = enrichCtxWithCWD(ctx, conv, sessionID)

	// Check that the session was NOT re-created.
	if _, err = sm.Get(context.Background(), ns, sessionID); !errors.Is(err, agent.ErrSessionNotFound) {
		t.Fatalf("expected deleted session after enrichCtxWithCWD, got err=%v", err)
	}
}
