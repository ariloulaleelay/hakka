package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/event"
)

type (
	vimCommandArgs struct {
		Command string `json:"command"`
	}
	vimReadBufferArgs struct {
		Bufnr int `json:"bufnr"`
	}
)

// VimRunCommand sends a Vim command to the Neovim client via the
// ClientWriter stored in the context, blocks for the response, and
// returns the result. It gives the agent full access to the user's
// Neovim instance: read buffers, edit buffers, run :! commands, etc.
//
// The tool requires a ClientWriter and ResponseReader to be present in
// the context (set by the gateway). If they are missing, the tool
// returns an error.
func VimRunCommand() agent.Tool {
	return NewTool("vim_run_command",
		"Execute a Lua command in the user's Neovim instance and return the result. Gives full access to the editor: read buffers, edit text, run Ex commands (`:!`), get cursor position, modify windows, etc. The command is any valid Lua code that returns a JSON-serializable value (number, string, table, etc.). Examples:\n  - `return vim.api.nvim_buf_get_lines(0, 0, -1, false)` — get current buffer contents\n  - `vim.api.nvim_buf_set_lines(0, 0, -1, false, {'new line'}); return 'ok'` — replace buffer\n  - `return vim.fn.expand('%:p')` — get current file path\n  - `return vim.bo.filetype` — get filetype\n  - `local lines = vim.api.nvim_buf_get_lines(0, 0, -1, false); return #lines` — line count\n  - `vim.cmd('write'); return 'saved'` — save the current buffer").
		StringParam("command", "A Lua expression/statement to execute in Neovim. Must return a JSON-serializable value (use `return ...` to get a result). For side-effect-only commands, use `return 'ok'`.", true).
		Tags("vim", "developer", "all").
		Timeout(30 * time.Second).
		ExecSnippet(execSnippetOneLine("command")).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args vimCommandArgs
			if err := unmarshalToolArgs(raw, "vim_run_command", &args); err != nil {
				return "", err
			}
			if args.Command == "" {
				slog.Warn("vim_run_command: empty command")
				return "", fmt.Errorf("vim_run_command: command is required")
			}

			result, err := sendClientRequest(ctx, args.Command)
			if err != nil {
				return "", fmt.Errorf("vim_run_command: %w", err)
			}

			// Format the result for the LLM: pretty-print JSON, or
			// return a null placeholder for empty responses.
			if len(result) == 0 {
				return `{"result": null}`, nil
			}
			var v any
			if json.Unmarshal(result, &v) == nil {
				pretty, _ := json.Marshal(v)
				return string(pretty), nil
			}
			return string(result), nil
		}).
		Build()
}

// sendClientRequest sends a Lua command to the Neovim client and returns
// the raw JSON result. It is a building block for vim_* tools.
func sendClientRequest(ctx context.Context, command string) (json.RawMessage, error) {
	cw := event.ClientFromContext(ctx)
	rr := event.ResponseReaderFromContext(ctx)
	if cw == nil || rr == nil {
		slog.Warn("vim tool: no client in context", "hasWriter", cw != nil, "hasReader", rr != nil)
		return nil, fmt.Errorf("vim tool: not connected to a Neovim client")
	}

	requestID := uuid.NewString()
	slog.Debug("vim tool: sending request", "requestID", requestID, "command", agent.Truncate(command, 80))

	if err := cw.WriteFrame(event.Frame{
		Event: "vim_request",
		ClientReq: &event.ClientRequest{
			RequestID: requestID,
			Command:   command,
		},
	}); err != nil {
		slog.Error("vim tool: failed to send", "requestID", requestID, "error", err)
		return nil, fmt.Errorf("vim tool: failed to send request: %w", err)
	}

	resp := rr.AwaitResponse(ctx, requestID)
	if resp == nil {
		slog.Error("vim tool: request cancelled or timed out", "requestID", requestID)
		return nil, fmt.Errorf("vim tool: request cancelled or timed out")
	}

	if resp.Error != "" {
		slog.Error("vim tool: client returned error", "requestID", requestID, "error", resp.Error)
		return nil, fmt.Errorf("vim tool: client error: %s", resp.Error)
	}

	return resp.Result, nil
}

// VimListBuffers lists all open Neovim buffers with their numbers, names,
// and file types. Requires a Neovim client connection.
func VimListBuffers() agent.Tool {
	const luaCommand = `local bufs = vim.api.nvim_list_bufs(); local info = {}; for _, b in ipairs(bufs) do if vim.api.nvim_buf_is_loaded(b) then table.insert(info, {nr=b, name=vim.api.nvim_buf_get_name(b), ft=vim.bo[b].filetype, modified=vim.api.nvim_buf_get_option(b, 'modified')}) end; end; return info`

	return NewTool("vim_list_buffers",
		"List all open buffers in the user's Neovim instance. Returns buffer number, name (file path), filetype, and modified status for each loaded buffer. Use this to discover what files the user is currently editing.").
		Tags("vim", "developer", "all").
		Timeout(10 * time.Second).
		ExecSnippet(func(args json.RawMessage) string {
			return "list_buffers"
		}).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			result, err := sendClientRequest(ctx, luaCommand)
			if err != nil {
				return "", err
			}

			var buffers []struct {
				Nr       int    `json:"nr"`
				Name     string `json:"name"`
				Ft       string `json:"ft"`
				Modified bool   `json:"modified"`
			}
			if err := json.Unmarshal(result, &buffers); err != nil {
				return "", fmt.Errorf("vim_list_buffers: failed to parse response: %w", err)
			}

			if len(buffers) == 0 {
				return `{"info":"no open buffers"}`, nil
			}

			var lines []string
			lines = append(lines, "Open buffers:")
			for _, b := range buffers {
				name := b.Name
				if name == "" {
					name = "[No Name]"
				}
				mod := ""
				if b.Modified {
					mod = " [modified]"
				}
				ft := b.Ft
				if ft == "" {
					ft = "?"
				}
				lines = append(lines, fmt.Sprintf("  buffer %d: %s  (%s)%s", b.Nr, name, ft, mod))
			}
			return strings.Join(lines, "\n"), nil
		}).
		Build()
}

// VimReadBuffer reads the contents of a specific Neovim buffer by its
// buffer number (bufnr). Returns the lines as text with line numbers.
func VimReadBuffer() agent.Tool {
	return NewTool("vim_read_buffer",
		"Read the contents of a specific Neovim buffer by its buffer number (bufnr). Use vim_list_buffers first to discover buffer numbers. Returns the buffer contents as numbered lines.").
		IntParam("bufnr", "The buffer number to read (e.g. 1, 2, 3). Use vim_list_buffers to discover available buffer numbers.", true).
		Tags("vim", "developer", "all").
		Timeout(10 * time.Second).
		ExecSnippet(func(args json.RawMessage) string {
			var params vimReadBufferArgs
			if err := json.Unmarshal(args, &params); err != nil || params.Bufnr == 0 {
				return ""
			}
			return fmt.Sprintf("bufnr=%d", params.Bufnr)
		}).
		Handler(func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args vimReadBufferArgs
			if err := unmarshalToolArgs(raw, "vim_read_buffer", &args); err != nil {
				return "", err
			}
			if args.Bufnr <= 0 {
				return "", fmt.Errorf("vim_read_buffer: bufnr is required and must be a positive integer")
			}

			command := fmt.Sprintf(`return vim.api.nvim_buf_get_lines(%d, 0, -1, false)`, args.Bufnr)
			result, err := sendClientRequest(ctx, command)
			if err != nil {
				return "", err
			}

			var lines []string
			if err := json.Unmarshal(result, &lines); err != nil {
				return "", fmt.Errorf("vim_read_buffer: failed to parse response: %w", err)
			}

			if len(lines) == 0 {
				return `{"info":"buffer is empty"}`, nil
			}

			var numbered []string
			for i, line := range lines {
				numbered = append(numbered, fmt.Sprintf("%6d  %s", i+1, line))
			}
			return strings.Join(numbered, "\n"), nil
		}).
		Build()
}
