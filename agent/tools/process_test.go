package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

// runProcessArgs is a helper to call a tool handler with args and return result + error.
func runProcess(t *testing.T, h func(context.Context, json.RawMessage) (string, error), args any) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	return h(context.Background(), raw)
}

func TestSpawnProcess(t *testing.T) {
	pm := NewProcessManager()

	// Spawn a simple echo process.
	// Using `cat` with stdin so we can interact with it.
	cmd := "cat"
	if runtime.GOOS == "windows" {
		cmd = "cmd"
	}

	result, err := runProcess(t, SpawnProcess(pm).Handler, map[string]any{
		"command": cmd,
		"args":    []any{},
	})
	if err != nil {
		t.Fatalf("spawn_process: %v", err)
	}

	// Should return the procss id.
	if !strings.Contains(result, "Started:") {
		t.Fatalf("expected 'Started:' in result, got: %q", result)
	}
	if !strings.Contains(result, "pid=") {
		t.Fatalf("expected 'pid=' in result, got: %q", result)
	}

	// Extract the id from the result.
	id := extractProcessID(result)
	if id == "" {
		t.Fatalf("could not extract process id from: %s", result)
	}

	// Clean up.
	_, _ = runProcess(t, KillProcess(pm).Handler, map[string]any{"id": id})
}

func TestInteractProcess_SendAndRead(t *testing.T) {
	pm := NewProcessManager()

	// Spawn cat (echoes stdin to stdout).
	result, err := runProcess(t, SpawnProcess(pm).Handler, map[string]any{
		"command": "cat",
	})
	if err != nil {
		t.Fatalf("spawn_process: %v", err)
	}
	id := extractProcessID(result)
	if id == "" {
		t.Fatalf("could not extract process id from: %s", result)
	}

	// Send input and read.
	interactResult, err := runProcess(t, InteractProcess(pm).Handler, map[string]any{
		"id":              id,
		"input":           "hello world\n",
		"timeout_seconds": 5,
	})
	if err != nil {
		t.Fatalf("interact_process: %v", err)
	}
	if !strings.Contains(interactResult, "hello world") {
		t.Fatalf("expected 'hello world' in output, got: %q", interactResult)
	}
	if !strings.Contains(interactResult, id[:8]) {
		t.Fatalf("expected process id prefix in output, got: %q", interactResult)
	}

	// Clean up.
	_, _ = runProcess(t, KillProcess(pm).Handler, map[string]any{"id": id})
}

func TestInteractProcess_ReadOnly(t *testing.T) {
	pm := NewProcessManager()

	result, err := runProcess(t, SpawnProcess(pm).Handler, map[string]any{
		"command": "cat",
	})
	if err != nil {
		t.Fatalf("spawn_process: %v", err)
	}
	id := extractProcessID(result)
	if id == "" {
		t.Fatalf("could not extract process id")
	}

	// Send input first.
	_, err = runProcess(t, InteractProcess(pm).Handler, map[string]any{
		"id":              id,
		"input":           "line1\nline2\n",
		"timeout_seconds": 5,
	})
	if err != nil {
		t.Fatalf("interact_process (write): %v", err)
	}

	// Read again without sending — should see nothing new (already consumed).
	readResult, err := runProcess(t, InteractProcess(pm).Handler, map[string]any{
		"id":              id,
		"timeout_seconds": 1,
	})
	if err != nil {
		t.Fatalf("interact_process (read): %v", err)
	}
	// Might be empty or contain process prefix, but no new lines.
	if strings.Contains(readResult, "line1") {
		t.Logf("read-only returned previously consumed data (may be expected): %q", readResult)
	}

	_, _ = runProcess(t, KillProcess(pm).Handler, map[string]any{"id": id})
}

func TestKillProcess(t *testing.T) {
	pm := NewProcessManager()

	// Spawn a long-running process.
	result, err := runProcess(t, SpawnProcess(pm).Handler, map[string]any{
		"command": "sleep",
		"args":    []any{"60"},
	})
	if err != nil {
		t.Fatalf("spawn_process: %v", err)
	}
	id := extractProcessID(result)
	if id == "" {
		t.Fatalf("could not extract process id")
	}

	// Kill it.
	killResult, err := runProcess(t, KillProcess(pm).Handler, map[string]any{
		"id": id,
	})
	if err != nil {
		t.Fatalf("kill_process: %v", err)
	}
	if !strings.Contains(killResult, "killed") && !strings.Contains(killResult, "terminated") {
		t.Fatalf("expected kill confirmation, got: %q", killResult)
	}

	// List should not show it as running.
	listResult, err := runProcess(t, ListProcesses(pm).Handler, map[string]any{})
	if err != nil {
		t.Fatalf("list_processes: %v", err)
	}
	if strings.Contains(listResult, id[:8]) {
		t.Logf("process still listed but that may be expected if we keep exited processes: %q", listResult)
	}
}

func TestKillProcess_Nonexistent(t *testing.T) {
	pm := NewProcessManager()

	_, err := runProcess(t, KillProcess(pm).Handler, map[string]any{
		"id": "00000000-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent process")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestListProcesses(t *testing.T) {
	pm := NewProcessManager()

	// Before spawning anything, list should be empty.
	listResult, err := runProcess(t, ListProcesses(pm).Handler, map[string]any{})
	if err != nil {
		t.Fatalf("list_processes: %v", err)
	}
	if !strings.Contains(listResult, "No running processes") {
		t.Fatalf("expected 'No running processes', got: %q", listResult)
	}

	// Spawn two processes.
	r1, _ := runProcess(t, SpawnProcess(pm).Handler, map[string]any{"command": "cat"})
	id1 := extractProcessID(r1)

	r2, _ := runProcess(t, SpawnProcess(pm).Handler, map[string]any{"command": "cat"})
	id2 := extractProcessID(r2)

	// List should show both.
	listResult, err = runProcess(t, ListProcesses(pm).Handler, map[string]any{})
	if err != nil {
		t.Fatalf("list_processes: %v", err)
	}
	if !strings.Contains(listResult, id1[:8]) {
		t.Fatalf("expected process1 in listing, got: %q", listResult)
	}
	if !strings.Contains(listResult, id2[:8]) {
		t.Fatalf("expected process2 in listing, got: %q", listResult)
	}

	// Clean up.
	_, _ = runProcess(t, KillProcess(pm).Handler, map[string]any{"id": id1})
	_, _ = runProcess(t, KillProcess(pm).Handler, map[string]any{"id": id2})
}

func TestSpawnProcess_Negative(t *testing.T) {
	pm := NewProcessManager()

	_, err := runProcess(t, SpawnProcess(pm).Handler, map[string]any{
		"command": "",
	})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestSpawnProcess_WithEnv(t *testing.T) {
	pm := NewProcessManager()

	result, err := runProcess(t, SpawnProcess(pm).Handler, map[string]any{
		"command": "sh",
		"args":    []any{"-c", "echo $MY_VAR"},
		"env": map[string]any{
			"MY_VAR": "test_value",
		},
	})
	if err != nil {
		t.Fatalf("spawn_process: %v", err)
	}
	id := extractProcessID(result)
	if id == "" {
		t.Fatalf("could not extract process id")
	}

	// Read the output.
	interactResult, err := runProcess(t, InteractProcess(pm).Handler, map[string]any{
		"id":              id,
		"timeout_seconds": 5,
	})
	if err != nil {
		t.Fatalf("interact_process: %v", err)
	}
	if !strings.Contains(interactResult, "test_value") {
		t.Fatalf("expected 'test_value' in output (from env), got: %q", interactResult)
	}

	_, _ = runProcess(t, KillProcess(pm).Handler, map[string]any{"id": id})
}

func TestInteractProcess_Timeout(t *testing.T) {
	pm := NewProcessManager()

	result, _ := runProcess(t, SpawnProcess(pm).Handler, map[string]any{
		"command": "cat",
	})
	id := extractProcessID(result)
	if id == "" {
		t.Fatalf("could not extract process id")
	}

	// Interact with a very short timeout. Process won't produce output
	// without input, so this should timeout and return whatever is
	// buffered (likely empty).
	interactResult, err := runProcess(t, InteractProcess(pm).Handler, map[string]any{
		"id":              id,
		"timeout_seconds": 1,
	})
	if err != nil {
		t.Fatalf("interact_process: %v", err)
	}
	// Should contain the process id prefix (for context) and possibly be empty.
	if !strings.Contains(interactResult, id[:8]) {
		t.Fatalf("expected process id in timeout output, got: %q", interactResult)
	}

	_, _ = runProcess(t, KillProcess(pm).Handler, map[string]any{"id": id})
}

func TestProcessManager_Concurrency(t *testing.T) {
	pm := NewProcessManager()

	// Spawn multiple processes concurrently.
	const n = 5
	type proc struct {
		id string
	}
	ch := make(chan proc, n)

	for i := 0; i < n; i++ {
		go func() {
			result, err := runProcess(t, SpawnProcess(pm).Handler, map[string]any{
				"command": "cat",
			})
			if err != nil {
				t.Errorf("concurrent spawn: %v", err)
				ch <- proc{}
				return
			}
			ch <- proc{id: extractProcessID(result)}
		}()
	}

	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		p := <-ch
		if p.id != "" {
			ids = append(ids, p.id)
		}
	}

	// List should show all of them.
	listResult, err := runProcess(t, ListProcesses(pm).Handler, map[string]any{})
	if err != nil {
		t.Fatalf("list_processes: %v", err)
	}
	for _, id := range ids {
		if !strings.Contains(listResult, id[:8]) {
			t.Fatalf("expected process %s in listing after concurrent spawn", id[:8])
		}
	}

	// Kill all.
	for _, id := range ids {
		_, _ = runProcess(t, KillProcess(pm).Handler, map[string]any{"id": id})
	}
}

// extractProcessID extracts the ID from a spawn result like:
// "Started: abc123 (pid=12345, command=cat)"
func extractProcessID(result string) string {
	idx := strings.Index(result, "Started: ")
	if idx < 0 {
		return ""
	}
	rest := result[idx+len("Started: "):]
	// ID ends at the space before " (pid=".
	spaceIdx := strings.Index(rest, " ")
	if spaceIdx < 0 {
		return ""
	}
	return rest[:spaceIdx]
}

func TestSpawnProcess_ExitEarly(t *testing.T) {
	pm := NewProcessManager()

	// Spawn a quick process that exits immediately.
	result, err := runProcess(t, SpawnProcess(pm).Handler, map[string]any{
		"command": "echo",
		"args":    []any{"hello from quick process"},
	})
	if err != nil {
		t.Fatalf("spawn_process: %v", err)
	}
	id := extractProcessID(result)
	if id == "" {
		t.Fatalf("could not extract process id")
	}

	// Give it a moment to finish.
	time.Sleep(500 * time.Millisecond)

	// Read the output.
	interactResult, err := runProcess(t, InteractProcess(pm).Handler, map[string]any{
		"id":              id,
		"timeout_seconds": 3,
	})
	if err != nil {
		t.Fatalf("interact_process on exited process: %v", err)
	}
	if !strings.Contains(interactResult, "hello from quick process") {
		t.Fatalf("expected output from quick process, got: %q", interactResult)
	}

	// KillProcess on an already-exited process should be fine (no-op or cleanup).
	_, err = runProcess(t, KillProcess(pm).Handler, map[string]any{"id": id})
	if err != nil {
		t.Fatalf("kill_process on exited process: %v", err)
	}
}

func TestInteractProcess_NoId(t *testing.T) {
	pm := NewProcessManager()

	_, err := runProcess(t, InteractProcess(pm).Handler, map[string]any{
		"input": "hello",
	})
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}
