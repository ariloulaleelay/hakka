package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
)

// stdioClient implements MCPClient over stdin/stdout of a subprocess.
type stdioClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner

	mu        sync.Mutex
	pending   map[string]chan jsonrpcResponse // key is the string representation of the JSON-RPC id
	nextID    atomic.Int64
	closeOnce sync.Once
	done      chan struct{}

	logger *slog.Logger
}

// NewStdioClient launches a subprocess and connects to it via stdio.
// The caller must call Close() when done.
//
// ctx is used for per-request timeouts (the context passed to each
// individual method call), but the subprocess itself is NOT tied to
// ctx — cancelling ctx does not kill the process. Use Close() to
// terminate the subprocess and release all resources.
func NewStdioClient(ctx context.Context, cfg ServerConfig) (*stdioClient, error) {
	logger := slog.Default()

	// IMPORTANT: use exec.Command (NOT CommandContext) so the subprocess
	// is not killed when ctx expires. We manage the process lifecycle
	// ourselves via Close().
	cmd := exec.Command(cfg.Command, cfg.Args...)

	// Set environment variables
	if len(cfg.Env) > 0 {
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcp stdio: stdout pipe: %w", err)
	}

	// Capture stderr and log it — MCP servers often write diagnostics there.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcp stdio: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcp stdio: start: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	// 4 MB buffer max line length
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	c := &stdioClient{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  scanner,
		pending: make(map[string]chan jsonrpcResponse),
		done:    make(chan struct{}),
		logger:  logger,
	}
	c.nextID.Store(1)

	// Log stderr output asynchronously
	go c.logStderr(stderr)

	// Start a goroutine to read responses from stdout
	go c.readLoop()

	return c, nil
}

// logStderr reads from the stderr pipe and logs each line.
func (c *stdioClient) logStderr(stderr io.ReadCloser) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		c.logger.Warn("mcp server stderr", "cmd", c.cmd.Path, "msg", line)
	}
	if err := scanner.Err(); err != nil {
		c.logger.Debug("mcp stderr reader finished", "cmd", c.cmd.Path, "err", err)
	}
}

// readLoop reads JSON-RPC responses from stdout and dispatches them to
// waiting callers.
func (c *stdioClient) readLoop() {
	defer close(c.done)

	for c.stdout.Scan() {
		line := c.stdout.Text()
		if line == "" {
			continue
		}

		var resp jsonrpcResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			// Non-JSON output? Skip it (some servers log to stdout).
			c.logger.Debug("mcp: non-JSON output on stdout (skipping)", "line", line)
			continue
		}

		idStr := string(resp.ID)

		c.mu.Lock()
		ch, ok := c.pending[idStr]
		if ok {
			delete(c.pending, idStr)
		}
		c.mu.Unlock()

		if ok && ch != nil {
			select {
			case ch <- resp:
			default:
			}
		}
	}

	// Scanner error or EOF — close all pending requests
	if err := c.stdout.Err(); err != nil {
		c.logger.Warn("mcp stdout scanner error", "cmd", c.cmd.Path, "err", err)
		c.mu.Lock()
		for id, ch := range c.pending {
			delete(c.pending, id)
			close(ch)
		}
		c.mu.Unlock()
	}
}

// sendRequest sends a JSON-RPC request and waits for the response.
func (c *stdioClient) sendRequest(ctx context.Context, method string, params json.RawMessage) (jsonrpcResponse, error) {
	id := json.RawMessage(fmt.Sprintf("%d", c.nextID.Add(1)))
	idStr := string(id)

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	raw, err := json.Marshal(req)
	if err != nil {
		return jsonrpcResponse{}, fmt.Errorf("mcp: marshal request: %w", err)
	}

	// Create a channel and register it before sending
	ch := make(chan jsonrpcResponse, 1)
	c.mu.Lock()
	c.pending[idStr] = ch
	c.mu.Unlock()

	// Ensure cleanup on error
	defer func() {
		c.mu.Lock()
		delete(c.pending, idStr)
		c.mu.Unlock()
	}()

	// Write the request
	if _, err := fmt.Fprintln(c.stdin, string(raw)); err != nil {
		return jsonrpcResponse{}, fmt.Errorf("mcp: write request: %w", err)
	}

	// Wait for response or context cancellation
	select {
	case <-ctx.Done():
		return jsonrpcResponse{}, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return jsonrpcResponse{}, fmt.Errorf("mcp: connection closed")
		}
		if resp.Error != nil {
			return resp, resp.Error
		}
		return resp, nil
	}
}

// Initialize performs the MCP initialization handshake.
func (c *stdioClient) Initialize(ctx context.Context) error {
	params := json.RawMessage(`{
		"protocolVersion": "2024-11-05",
		"capabilities": {},
		"clientInfo": {
			"name": "hakka",
			"version": "0.1.0"
		}
	}`)
	resp, err := c.sendRequest(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("mcp initialize: %w", err)
	}
	_ = resp // we don't need the response for now
	return nil
}

// ListTools returns the list of tools advertised by the server.
func (c *stdioClient) ListTools(ctx context.Context) ([]mcpTool, error) {
	resp, err := c.sendRequest(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}

	var result struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp tools/list: decode result: %w", err)
	}
	return result.Tools, nil
}

// CallTool invokes a tool on the server.
func (c *stdioClient) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	params := map[string]any{
		"name":      name,
		"arguments": json.RawMessage(args),
	}
	paramsRaw, _ := json.Marshal(params)

	resp, err := c.sendRequest(ctx, "tools/call", paramsRaw)
	if err != nil {
		return "", fmt.Errorf("mcp tools/call: %w", err)
	}

	var result mcpCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("mcp tools/call: decode result: %w", err)
	}

	if result.IsError {
		return "", fmt.Errorf("mcp tool %q returned error: %s", name, result.toPlainText())
	}

	return result.toPlainText(), nil
}

// Close terminates the subprocess and cleans up.
// It ensures both the subprocess and the readLoop goroutine
// are fully terminated before returning.
func (c *stdioClient) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// Close stdin to signal the subprocess to exit cleanly
		if e := c.stdin.Close(); e != nil {
			err = e
		}
		// Kill the process as a safety net. Even if the subprocess
		// ignores stdin EOF (e.g. a misbehaving MCP server), this
		// ensures stdout/stderr pipes are closed and readLoop can
		// unblock from scanner.Scan().
		if c.cmd.Process != nil {
			if e := c.cmd.Process.Kill(); e != nil {
				c.logger.Warn("mcp: process kill", "cmd", c.cmd.Path, "err", e)
			}
		}
		// Wait for the process to fully exit (reap any zombie state)
		if e := c.cmd.Wait(); e != nil {
			if err == nil {
				err = e
			}
		}
		// Wait for readLoop goroutine to finish. After the process is
		// dead, stdout is closed, so readLoop's scanner will get EOF
		// and the goroutine will exit — this drain ensures no goroutine
		// leak when Close() returns.
		<-c.done
	})
	return err
}
