package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

// ---------------------------------------------------------------------------
// ProcessManager — goroutine-safe registry of spawned subprocesses.
// ---------------------------------------------------------------------------

// ProcessState describes the lifecycle state of a managed process.
type ProcessState string

const (
	ProcessRunning ProcessState = "running"
	ProcessExited  ProcessState = "exited"
	ProcessRemoved ProcessState = "removed"
)

// ManagedProcess holds the runtime state of a spawned subprocess.
type ManagedProcess struct {
	ID        string
	Command   string
	Args      []string
	Pid       int
	State     ProcessState
	ExitCode  int
	CreatedAt time.Time

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	output *processBuffer
	cancel context.CancelFunc
	done   chan struct{}
}

// processBuffer captures stdout+stderr in a ring buffer with a cursor
// tracking the "last read" position, so each interact_process call
// returns only new data since the last read.
type processBuffer struct {
	mu         sync.Mutex
	data       []byte
	maxSize    int // ring buffer max size
	readCursor int // offset up to which data has been consumed
}

func newProcessBuffer(maxSize int) *processBuffer {
	return &processBuffer{
		data:    make([]byte, 0, maxSize),
		maxSize: maxSize,
	}
}

func (pb *processBuffer) Write(p []byte) (int, error) {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	if len(p) >= pb.maxSize {
		// New data is larger than our buffer — keep only the tail.
		pb.data = make([]byte, pb.maxSize)
		copy(pb.data, p[len(p)-pb.maxSize:])
		// Adjust the read cursor: if it was pointing into the buffer,
		// clamp it.
		if pb.readCursor > pb.maxSize {
			pb.readCursor = 0
		}
		return len(p), nil
	}

	// Room available?
	available := pb.maxSize - len(pb.data)
	if len(p) <= available {
		pb.data = append(pb.data, p...)
	} else {
		// Need to evict oldest data.
		excess := len(p) - available
		pb.data = append(pb.data[:pb.maxSize-len(p)], p...)
		pb.data = pb.data[excess:]
	}
	// Clamp read cursor.
	if pb.readCursor > len(pb.data) {
		pb.readCursor = 0
	}
	return len(p), nil
}

// ReadNew returns data written since the last call to ReadNew, and advances
// the cursor. Returns empty slice if nothing new.
func (pb *processBuffer) ReadNew() []byte {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	if pb.readCursor >= len(pb.data) {
		return nil
	}
	newData := make([]byte, len(pb.data)-pb.readCursor)
	copy(newData, pb.data[pb.readCursor:])
	pb.readCursor = len(pb.data)
	return newData
}

// PeekAll returns all data in the buffer without advancing the cursor.
func (pb *processBuffer) PeekAll() []byte {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	out := make([]byte, len(pb.data))
	copy(out, pb.data)
	return out
}

// ProcessManager manages all spawned subprocesses.
type ProcessManager struct {
	mu        sync.Mutex
	processes map[string]*ManagedProcess
	logger    *slog.Logger
}

// NewProcessManager creates a new ProcessManager.
func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		processes: make(map[string]*ManagedProcess),
		logger:    slog.Default(),
	}
}

// Spawn starts a new subprocess and registers it.
func (pm *ProcessManager) Spawn(ctx context.Context, command string, args []string, cwd string, env map[string]string) (*ManagedProcess, error) {
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}

	// Resolve CWD.
	if cwd == "" {
		cwd = event.CWDFromContext(ctx)
	}

	procID := agent.MakeUniqueID()

	cmd := exec.CommandContext(context.Background(), command, args...)

	// Set working directory.
	if cwd != "" {
		cmd.Dir = cwd
	}

	// Set up environment: inherit current env + add custom vars.
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	// Create stdin pipe.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	// Capture stdout + stderr into the ring buffer.
	buf := newProcessBuffer(64 * 1024) // 64KB ring buffer
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start: %w", err)
	}

	proc := &ManagedProcess{
		ID:        procID,
		Command:   command,
		Args:      args,
		Pid:       cmd.Process.Pid,
		State:     ProcessRunning,
		CreatedAt: time.Now(),
		cmd:       cmd,
		stdin:     stdin,
		output:    buf,
		done:      make(chan struct{}),
	}

	// Register before starting readers so the process is immediately findable.
	pm.mu.Lock()
	pm.processes[procID] = proc
	pm.mu.Unlock()

	// Background goroutine: read stdout and stderr into buffer.
	_, cancel := context.WithCancel(context.Background())
	proc.cancel = cancel

	go func() {
		defer close(proc.done)
		defer cancel()

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			_, _ = io.Copy(buf, stdout)
		}()
		go func() {
			defer wg.Done()
			_, _ = io.Copy(buf, stderr)
		}()

		wg.Wait()

		// Wait for the process to finish.
		err := cmd.Wait()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}

		pm.mu.Lock()
		proc.State = ProcessExited
		proc.ExitCode = exitCode
		pm.mu.Unlock()

		_ = stdin.Close()
	}()

	return proc, nil
}

// Get returns a process by ID.
func (pm *ProcessManager) Get(id string) (*ManagedProcess, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	p, ok := pm.processes[id]
	return p, ok
}

// Kill sends a signal to a process. If signal is empty, sends SIGTERM.
// On Windows, we use cmd.Process.Kill() which sends SIGKILL.
func (pm *ProcessManager) Kill(id string, signal string) error {
	pm.mu.Lock()
	proc, ok := pm.processes[id]
	pm.mu.Unlock()

	if !ok {
		return fmt.Errorf("process not found: %s", id)
	}

	if proc.State == ProcessExited || proc.State == ProcessRemoved {
		// Already gone — just clean up.
		pm.remove(id)
		return nil
	}

	if runtime.GOOS == "windows" || signal == "" || signal == "SIGTERM" || signal == "TERM" {
		if err := proc.cmd.Process.Signal(os.Interrupt); err != nil {
			// If signal fails, fall back to Kill.
			_ = proc.cmd.Process.Kill()
		}
	} else if signal == "SIGKILL" || signal == "KILL" {
		_ = proc.cmd.Process.Kill()
	} else {
		return fmt.Errorf("unsupported signal: %s (supported: SIGTERM, SIGKILL)", signal)
	}

	// Wait for process to exit (with timeout).
	select {
	case <-proc.done:
	case <-time.After(3 * time.Second):
		_ = proc.cmd.Process.Kill()
		<-proc.done
	}

	pm.remove(id)
	return nil
}

// List returns a copy of all processes.
func (pm *ProcessManager) List() []ManagedProcess {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	out := make([]ManagedProcess, 0, len(pm.processes))
	for _, p := range pm.processes {
		out = append(out, *p)
	}
	return out
}

// remove unregisters a process by ID.
func (pm *ProcessManager) remove(id string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.processes, id)
}

// WriteStdin writes data to the process's stdin.
func (pm *ProcessManager) WriteStdin(id, input string) error {
	pm.mu.Lock()
	proc, ok := pm.processes[id]
	pm.mu.Unlock()

	if !ok {
		return fmt.Errorf("process not found: %s", id)
	}
	if proc.State == ProcessExited || proc.State == ProcessRemoved {
		return fmt.Errorf("process %s is not running (state=%s)", id[:8], proc.State)
	}

	_, err := io.WriteString(proc.stdin, input)
	return err
}

// ReadNewOutput returns new output since the last read.
func (pm *ProcessManager) ReadNewOutput(id string) ([]byte, bool) {
	pm.mu.Lock()
	proc, ok := pm.processes[id]
	pm.mu.Unlock()

	if !ok {
		return nil, false
	}
	return proc.output.ReadNew(), true
}

// WaitForOutput waits up to timeout for new output to appear.
// Returns new output since last read (may be empty on timeout).
func (pm *ProcessManager) WaitForOutput(id string, timeout time.Duration) []byte {
	// Poll for new output.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pm.mu.Lock()
		proc, ok := pm.processes[id]
		pm.mu.Unlock()

		if !ok {
			return nil
		}

		// Check if process has exited.
		if proc.State == ProcessExited || proc.State == ProcessRemoved {
			return proc.output.ReadNew()
		}

		data := proc.output.ReadNew()
		if len(data) > 0 {
			return data
		}

		time.Sleep(50 * time.Millisecond)
	}

	// Timeout — return whatever is buffered (even if empty).
	pm.mu.Lock()
	proc, ok := pm.processes[id]
	pm.mu.Unlock()
	if ok {
		return proc.output.ReadNew()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

// SpawnProcess creates the spawn_process tool.
func SpawnProcess(pm *ProcessManager) agent.Tool {
	return NewTool("spawn_process",
		"Start a new subprocess and return its unique ID for subsequent interaction. "+
			"The process can be interacted with via `interact_process`, terminated via `kill_process`, "+
			"and listed via `list_processes`. "+
			"The process runs in the background; use `interact_process` to send input and read output.").
		StringParam("command", "Command to execute (e.g. 'gdb', 'python', 'cat')", true).
		ObjectParam("args", "Command-line arguments as an array of strings (optional)", false).
		StringParam("cwd", "Working directory for the process (optional)", false).
		ObjectParam("env", "Environment variables as a map of KEY=VALUE strings (optional)", false).
		Tags("process", "exec", "developer", "all").
		ExecSnippet(func(args json.RawMessage) string {
			var params struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			}
			if err := json.Unmarshal(args, &params); err != nil || params.Command == "" {
				return ""
			}
			s := params.Command
			if len(params.Args) > 0 {
				s += " " + strings.Join(params.Args, " ")
			}
			return s
		}).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Cwd     string            `json:"cwd"`
				Env     map[string]string `json:"env"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("spawn_process: %w", err)
			}

			proc, err := pm.Spawn(ctx, args.Command, args.Args, args.Cwd, args.Env)
			if err != nil {
				return "", fmt.Errorf("spawn_process: %w", err)
			}

			cmdStr := args.Command
			if len(args.Args) > 0 {
				cmdStr += " " + strings.Join(args.Args, " ")
			}

			return fmt.Sprintf("Started: %s (pid=%d, command=%s, state=%s)",
				proc.ID, proc.Pid, cmdStr, proc.State), nil
		}).
		Build()
}

// InteractProcess creates the interact_process tool.
func InteractProcess(pm *ProcessManager) agent.Tool {
	return NewTool("interact_process",
		"Send input to a running process and/or read its pending output. "+
			"This is the primary way to communicate with a spawned process. "+
			"Each call returns only the output that has accumulated since the last read. "+
			"To send input: use the `input` parameter. "+
			"To just read pending output without sending anything: omit `input`. "+
			"Use `timeout_seconds` to control how long to wait for output after sending input "+
			"(default 30, 0 = return immediately with whatever is buffered). "+
			"The output includes both stdout and stderr, merged in chronological order.").
		StringParam("id", "Process ID returned by spawn_process", true).
		StringParam("input", "Text to send to the process's stdin (optional). If omitted, just reads pending output.", false).
		IntParam("timeout_seconds", "How long to wait for output after sending input (default 30, 0 = return immediately)", false).
		Tags("process", "exec", "developer", "all").
		ExecSnippet(func(args json.RawMessage) string {
			var params struct {
				ID    string `json:"id"`
				Input string `json:"input"`
			}
			if err := json.Unmarshal(args, &params); err != nil || params.ID == "" {
				return ""
			}
			s := params.ID[:min(len(params.ID), 8)]
			if params.Input != "" {
				s += " input=" + params.Input
			}
			return s
		}).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				ID      string `json:"id"`
				Input   string `json:"input"`
				Timeout int    `json:"timeout_seconds"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("interact_process: %w", err)
			}
			if args.ID == "" {
				return "", fmt.Errorf("interact_process: id is required")
			}

			// Verify the process exists.
			if _, ok := pm.Get(args.ID); !ok {
				return "", fmt.Errorf("interact_process: process not found: %s", args.ID)
			}

			// Send input if provided.
			if args.Input != "" {
				if err := pm.WriteStdin(args.ID, args.Input); err != nil {
					return "", fmt.Errorf("interact_process: %w", err)
				}
			}

			// Wait for output if timeout > 0.
			var output []byte
			if args.Timeout > 0 {
				timeout := time.Duration(args.Timeout) * time.Second
				output = pm.WaitForOutput(args.ID, timeout)
			} else {
				output, _ = pm.ReadNewOutput(args.ID)
			}
			if output == nil {
				output = []byte{}
			}

			// Check process state.
			proc, _ := pm.Get(args.ID)
			stateInfo := ""
			if proc != nil && proc.State == ProcessExited {
				stateInfo = fmt.Sprintf("  [process exited with code %d]\n", proc.ExitCode)
			}

			shortID := args.ID[:min(len(args.ID), 8)]
			if len(output) == 0 {
				if stateInfo != "" {
					return fmt.Sprintf("Process %s...:\n%s  (no new output)", shortID, stateInfo), nil
				}
				return fmt.Sprintf("Process %s...:\n  (no new output)", shortID), nil
			}

			result := fmt.Sprintf("Process %s...:\n%s", shortID, stateInfo)
			result += string(output)
			return result, nil
		}).
		Build()
}

// KillProcess creates the kill_process tool.
func KillProcess(pm *ProcessManager) agent.Tool {
	return NewTool("kill_process",
		"Terminate a running process. "+
			"Sends SIGTERM by default (or SIGKILL if specified). "+
			"If the process has already exited, this is a no-op cleanup.").
		StringParam("id", "Process ID returned by spawn_process", true).
		StringParam("signal", "Signal to send: 'SIGTERM' (default) or 'SIGKILL' (optional)", false).
		Tags("process", "exec", "developer", "all").
		ExecSnippet(func(args json.RawMessage) string {
			var params struct {
				ID     string `json:"id"`
				Signal string `json:"signal"`
			}
			if err := json.Unmarshal(args, &params); err != nil || params.ID == "" {
				return ""
			}
			s := params.ID[:min(len(params.ID), 8)]
			if params.Signal != "" {
				s += " " + params.Signal
			}
			return s
		}).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				ID     string `json:"id"`
				Signal string `json:"signal"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("kill_process: %w", err)
			}
			if args.ID == "" {
				return "", fmt.Errorf("kill_process: id is required")
			}

			if args.Signal == "" {
				args.Signal = "SIGTERM"
			}

			if err := pm.Kill(args.ID, args.Signal); err != nil {
				return "", fmt.Errorf("kill_process: %w", err)
			}

			shortID := args.ID[:min(len(args.ID), 8)]
			return fmt.Sprintf("Process %s... killed with %s", shortID, args.Signal), nil
		}).
		Build()
}

// ListProcesses creates the list_processes tool.
func ListProcesses(pm *ProcessManager) agent.Tool {
	return NewTool("list_processes",
		"List all spawned processes with their ID, command, PID, state, and uptime. "+
			"Useful to discover active process IDs before interacting with them.").
		Tags("process", "exec", "developer", "all").
		ExecSnippet(func(args json.RawMessage) string {
			return "list"
		}).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			procs := pm.List()
			if len(procs) == 0 {
				return "No running processes", nil
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Processes (%d total):\n", len(procs)))
			b.WriteString(fmt.Sprintf("%-38s %-8s %-10s %-12s %s\n", "ID", "PID", "State", "Uptime", "Command"))
			b.WriteString(strings.Repeat("-", 80) + "\n")

			for _, p := range procs {
				uptime := time.Since(p.CreatedAt).Truncate(time.Second).String()
				uptimeStr := uptime
				if p.State == ProcessExited {
					uptimeStr = fmt.Sprintf("%s (exited)", uptime)
				}
				cmdStr := p.Command
				if len(p.Args) > 0 {
					cmdStr += " " + strings.Join(p.Args, " ")
				}
				pidStr := fmt.Sprintf("%d", p.Pid)
				if p.State == ProcessExited {
					pidStr = fmt.Sprintf("exit(%d)", p.ExitCode)
				}
				b.WriteString(fmt.Sprintf("%-38s %-8s %-10s %-12s %s\n",
					p.ID, pidStr, string(p.State), uptimeStr, cmdStr))
			}
			return b.String(), nil
		}).
		Build()
}
