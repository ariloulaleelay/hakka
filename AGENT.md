---
---
---
# Hakka — LLM Agent Core Framework

## Project Description

Hakka is a **minimal, modular, extensible LLM agent core framework** written in Go. It provides an orchestration engine that drives the LLM ↔ tool iteration loop, with pluggable model providers, persistent sessions, and transport-agnostic gateways (WebSocket, Telegram). It ships with a first-class Neovim plugin (`nvim/hakka.nvim`) that turns Neovim into an interactive agent IDE.

**Key design principles:**

- **Minimal Core, Maximum Extensibility** — Every component (LLM provider, persistence, tool, transport) is defined via Go interfaces.
- **Transport Agnosticism** — The engine is completely isolated from how the user connects (WebSocket, Telegram, Neovim).
- **Session Autonomy** — Sessions are first-class entities that live independently of any client. Clients connect, subscribe, and disconnect; sessions persist and continue processing.
- **Rich Client Observability** — Structured lifecycle hooks (tool call start/success/failure) let clients like Neovim render agent actions interactively.

---

## Brief Architecture

```
                         ┌─────────────────────────────────────┐
                         │           GATEWAYS                  │
                         │  WebSocket · Telegram               │
                         └──────────┬──────────────────────────┘
                                    │
                         ┌──────────▼──────────────────────────┐
                         │          ENGINE LAYER               │
                         │    Conversation (tool loop)         │
                         │    ├─ TurnRunner (iteration)        │
                         │    ├─ ToolExecutor (concurrent)     │
                         │    └─ AutoRenamer (naming)          │
                         │    StreamSession (stream+fallback)  │
                         │    commands/ (slash-commands)       │
                         └──────────┬──────────────────────────┘
                                    │
              ┌─────────────────────┼─────────────────────┐
              ▼                     ▼                     ▼
   ┌──────────────────┐   ┌──────────────┐   ┌──────────────────────────┐
   │  ADAPTERS        │   │ SESSION      │   │  TOOLS                   │
   │  OpenAI          │   │ STORE        │   │  read_file               │
   │  Anthropic       │   │  Memory      │   │  write_file              │
   │  Gemini          │   │  SQLite      │   │  edit_file               │
   └──────────────────┘   └──────────────┘   │  list_dir                │
                                             │  shell                   │
                                             │  http_get                │
                                             │  search (ripgrep)        │
                                             │  vim_run_command         │
                                             │  spawn_process           │
                                             │  interact_process        │
                                             │  kill_process            │
                                             │  list_processes          │
                                             └──────────────────────────┘
```

### Core Components

| Component | File(s) | Role |
|---|---|---|
| **Session** | `agent/session.go` | Append-only conversation history, system prompt, thread-safe state bag, model binding, token tracking, tool authorization |
| **SessionStore** | `agent/store.go` | Pluggable persistence — `MemoryStore` (in-process) or `sqlite.Store` (disk-backed via `modernc.org/sqlite`, CGO-free) |
| **SessionManager** | `agent/manager.go` | Wraps a store with `GetOrCreate`/`Save`/`Drop` helpers |
| **SessionView** | `agent/session_view.go` | Role-specific session interfaces (ISP) — `SessionHistory`, `SessionToolAuth`, `SessionModelBinding`, `SessionIdentity` |
| **LLMAdapter** | `agent/adapter.go` | Interface: `Complete()` + `Stream()`. Implementations in `agent/adapters/` for OpenAI, Anthropic, Gemini |
| **Registry** | `agent/registry.go` | Named collection of adapters with a default selection |
| **Router** | `agent/router.go` | Decides which adapter serves a given session based on the session's model binding. Single owner of the `Model` field |
| **Tool / ToolRegistry** | `agent/tool.go` | Tool schema + handler + optional timeout + exec snippet. Concurrent-safe registry |
| **Conversation** | `agent/conversation.go` | High-level public API for gateways — session lifecycle, turn dispatch, auto-rename. Delegates the LLM↔tool loop internals to `turnRunner` |
| **TurnRunner** | `agent/turn_runner.go` | Drives the LLM ↔ tool iteration loop (extracted from Conversation per SRP). Owns the step function, compaction limit resolution, and compactify schema augmentation |
| **ToolExecutor** | `agent/tool_executor.go` | Concurrent tool execution with fan-out, event hooks, and result appending |
| **Compact** | `agent/compact.go` | LLM-driven context compaction — when estimated context exceeds the soft limit, [N] message indices and a warning prompt the LLM to call `context_compactify` |
| **StreamSession** | `agent/stream_session.go` | Cooperative streaming — streams text tokens, transparently falls back to tool loop when model requests tools |
| **CommandProcessor** | `agent/commands/` | Structured JSON-only command handler (`ExecuteJSON`) — no text slash-command parsing. Delegates to ModelCommands, SessionCommands, ToolCommands. Clients map slash-commands to JSON commands locally. |
| **AutoRenamer** | `agent/auto_renamer.go` | LLM-driven session naming logic |
| **ProcessManager** | `agent/tools/process.go` | Goroutine-safe subprocess registry with ring-buffered stdout+stderr |
| **Gateway** | `agent/gateways/` | Transport layer — WebSocket (`websocket.go`), Telegram (`telegram.go`). Wire framing in `frame.go`. |
| **ResponseReader** | `agent/gateways/response_reader.go` | Async response awaiting for client-initiated tool calls (e.g. Neovim `vim_run_command`) |
| **TurnTracker** | `agent/gateways/turn_tracker.go` | Manages active turns per session — allows reconnection, fan-out to multiple subscribers, and cancellation |
| **Hooks** | `agent/engine.go` | Lifecycle callbacks: `OnToolCall`, `OnToolResult`, `OnLLMResponse`, `OnError` |
| **EngineEvent types** | `agent/event/event.go` | Typed event structs (`ToolCallStarted`, `ToolCallFinished`, `UsageReported`, `TextDelta`, etc.) |
| **EngineChannelWriter** | `agent/event/engine_writer.go` | Serialises tool-to-client requests through the engine event channel |
| **Format** | `agent/format/md2tg.go` | Markdown-to-Telegram-HTML conversion for the Telegram gateway |
| **Batch** | `batch/batch.go` | Reusable API for running Hakka in batch mode — autonomous tasks with no interactive transport |

### Built-in Tools (`agent/tools/`)

| Tool | Description |
|---|---|
| `read_file` | Read UTF-8 text files; truncates at 200K bytes with `[TRUNCATED <n> bytes]` marker |
| `write_file` | Create/overwrite files; creates parent directories automatically |
| `edit_file` | Literal string search-and-replace (first or all occurrences) |
| `list_dir` | List directory entries (dirs as `name/`, files as `name\t<size>`) |
| `shell` | Execute `sh -c` commands; short output inlined, large output saved to tempfiles |
| `http_get` | HTTP GET with headers, status, headers, and body; HTML converted to Markdown |
| `feedback` | Submit anonymous feedback (feature request or bug report) |
| `random` | Generate a random integer between min_value and max_value (inclusive) |
| `search` | ripgrep recursive search with file:line:col output |
| `vim_run_command` | Execute Lua in the user's Neovim instance (requires capable client) |
| `vim_list_buffers` | List open Neovim buffers with numbers, names, and filetypes |
| `vim_read_buffer` | Read a Neovim buffer by number as numbered lines |
| `spawn_process` | Start a subprocess and return its unique ID for subsequent interaction |
| `interact_process` | Send input to a running process and/or read its pending output |
| `kill_process` | Terminate a running process (SIGTERM by default, SIGKILL available) |
| `list_processes` | List all spawned processes with ID, PID, state, uptime, and command |
| `session_list` | List all sessions in the namespace with metadata |
| `session_rename` | Rename a session by ID or unique prefix |
| `session_info` | Show detailed session info |
| `session_read` | Read messages from a session, with optional role filter and limit |
| `session_search` | Search for text across all sessions in the namespace |
| `session_summarize` | Summarise a session's conversation (LLM-based or heuristic) |
| `session_ask_question` | Ask an LLM a question about a session's history |
| `session_delete` | Delete a session by ID or unique prefix |
| `session_create` | Create a new empty session |
| `context_compactify` | Compact message ranges to free context space (meta-tool) |
| `show_tool` | Show detailed info about a specific tool; also enables it |

`show_tool` is **pre-enabled** in every new session so the LLM can always discover and enable tools at runtime.

### Namespace (Realm) Model

Sessions are isolated into **namespaces** (realms). Each namespace is a logically separate
set of sessions — sessions in one namespace are invisible to clients in another.

| Gateway | Namespace | Rationale |
|---|---|---|
| WebSocket | `"default"` | All WS clients share one session pool. |
| Telegram | `"tg:<chat_id>"` | Each chat is its own isolated realm — one user's sessions are invisible to another. |

In the future, namespaces will become identity-based access scopes, allowing the same
user to access their sessions from both WebSocket and Telegram. For now, the mapping
is gateway-fixed.

---

## Wire Protocol

Hakka uses a **WebSocket JSON API**. Every message in both directions is a
single JSON object (one per WebSocket text frame). All fields are optional
unless noted otherwise.

### Connection & Session Lifecycle (for client authors)

A typical client follows this sequence:

1. **Connect** — open a WebSocket to `/ws`.
2. **Discover sessions** — send `{"type":"list_sessions"}` to see available sessions.
3. **Pick or create a session**:
   - To join an existing session: send any request with its `session_id`.
   - To create a new one: send `{"command":{"cmd":"session_create"}}`.
4. **Set working directory** — `{"command":{"cmd":"cwd_set","params":{"cwd":"/path"}}}`.
5. **Enable tools** — `{"command":{"cmd":"tool_allow","params":{"name":"#all"}}}`.
6. **Send input** — `{"session_id":"...","input":"Hello!","stream":true}`.
7. **Receive responses** — deltas, tool events, and finally `done: true`.
8. **Fetch a session** — `{"command":{"cmd":"get_session","params":{"id":"..."}}}`.

**Key rules for client authors:**

- Text-based slash commands (like `/help`, `/model`, `/session list`) are **not**
  intercepted by the server. Clients are responsible for parsing slash commands
  and converting them to structured JSON commands (see the `command` field below).
  The Telegram gateway includes a built-in `parseSlashCommand()` function for this.
- Always include `session_id` on every request once you have one.
  Omitting it on the first request auto-creates a session (backward compatibility).
- `stream: true` gives incremental `delta` frames; `stream: false` (or omitted)
  gives a single `output` + `done` frame at the end.
- The `done: true` frame **always** arrives — even on errors, even after cancel.
  It is your signal that the turn is complete and the engine is ready for new input.
- Tool events (`event: "tool"`) and meta events (`event: "meta"`) may arrive
  interleaved with deltas. A stream may produce `delta`, `tool`, `delta`, `meta`,
  `delta`, `done` in any order. Buffer deltas and accumulate the full response;
  render tool events at the position they occurred in the stream.
- If the LLM calls `vim_run_command`, you will receive a `client_request` event.
  You **must** reply with `{"type":"response","request_id":"...","result":...}`
  or the tool will block until timeout. If your client does not support Neovim,
  reply with `{"type":"response","request_id":"...","error":"unsupported"}`.
- Cancel an in-flight turn by sending `{"type":"cancel","session_id":"..."}`.
  The server replies with `{"event":"cancel","done":true,"data":{"cancelled":true}}`.
- Set CWD before sending substantive input. CWD persists on the session and is
  injected as a system message into every turn's context. Send `cwd_set` again
  whenever the user changes directory.

---

### Inbound Frames (Client → Server)

All frames share the base fields in `FrameRequest`:

| Field | Type | Description |
|---|---|---|
| `type` | string | `"request"` (default), `"cancel"`, `"response"`, `"list_sessions"` |
| `session_id` | string | Session UUID. Omit to auto-create; always send thereafter. |
| `input` | string | User text. Sent directly to the LLM — slash commands are **not** intercepted server-side; clients must use the `command` field for structured commands. |
| `stream` | bool | `true` for token-by-token deltas. |
| `cwd` | string | Client's working directory. Preferred: use `cwd_set` command instead. |
| `command` | object | Structured JSON command — see table below. |
| `request_id` | string | Only with `type: "response"` — echoes the `request_id` from a `client_request`. |
| `result` | any | Only with `type: "response"` — the JSON result from client. |
| `error` | string | Only with `type: "response"` — error message from client. |

**Frame types:**

| `type` | Purpose |
|---|---|
| *(omitted)* | Normal chat turn (sends `input` to the LLM directly — no slash-command interception). |
| `"cancel"` | Cancel the in-flight turn for `session_id`. |
| `"response"` | Client reply to a `client_request` (Neovim Lua eval result). |
| `"list_sessions"` | Discover sessions in the namespace. Server replies with `command_result` + session list. |

**Structured JSON commands** (`command` object with `cmd` + optional `params`):

| `cmd` | `params` | Description |
|---|---|---|
| `help` | — | List available commands. |
| `continue` | — | Continue LLM turn without new user input. |
| `start` | — | Create fresh session with **all tools enabled**. |
| `compact` | `{"n": <int>}` | Set context soft limit in tokens (0 = off). Omit params to read current value. |
| `cwd_set` | `{"cwd": "/path"}` | Persist working directory on the session. |
| `session_list` | — | List all sessions in the namespace. |
| `session_create` | — | Create a new empty session. |
| `get_session` | `{"id": "<uuid or prefix>"}` | Fetch session data and messages by ID. **Fails** if not found. |
| `session_delete` | `{"id": "<uuid>"}` | Delete a session. Use `"this"` for the current session. |
| `session_info` | — | Show current session details. |
| `session_rename` | `{"name": "..."}` | Rename the current session. |
| `session_autorename` | — | Ask the LLM to generate a session name. |
| `model_list` | — | List registered models (current marked with `current: true`). |
| `model_switch` | `{"name": "..."}` | Switch the session to a different model. |
| `tool_list` | — | List tools with `enabled`/`allowed`/`denied` status and tags. |
| `tool_allow` | `{"name": "tool_or_#tag"}` | Allow and enable a tool or all tools with a `#tag`. |
| `tool_deny` | `{"name": "tool_or_#tag"}` | Deny (hide) a tool or all tools with a `#tag`. |

**Tool tags** for use with `tool_allow`/`tool_deny`:

| Tag | Included tools |
|---|---|
| `filesystem` | `read_file`, `list_dir`, `write_file`, `edit_file`, `search` |
| `network` | `http_get` |
| `exec` | `shell` |
| `utility` | `random`, `feedback`, session tools |
| `vim` | `vim_list_buffers`, `vim_read_buffer`, `vim_run_command` |
| `session` | All session management tools |
| `all` | Every registered tool |

### Outbound Frames (Server → Client)

All frames share the base fields in `FrameResponse`:

| Field | Type | Description |
|---|---|---|
| `session_id` | string | Session this frame belongs to. |
| `output` | string | Full assistant reply (non-streaming only). |
| `delta` | string | Streaming text chunk. |
| `done` | bool | **Terminal frame** — when `true`, the turn is complete and the engine is ready for new input. Always emitted. |
| `error` | string | Error message. When present, the turn failed. |
| `event` | string | Event type: `"tool"`, `"meta"`, `"command_result"`, `"get_session"`, `"session_create"`, `"session_renamed"`, `"cancel"`, `"client_request"`. |
| `tool` | string | Tool name (only with `event: "tool"`). |
| `status` | string | Tool status: `"start"`, `"ok"`, `"err"` (only with `event: "tool"`). |
| `args` | object | Structured tool arguments as JSON object. |
| `exec_snippet` | string | Human-readable one-liner of what the tool call does. |
| `data` | object | Payload for `meta`, `command_result`, `cancel`, and `session_*` events. |
| `cmd` | string | Command name that produced this `command_result`. |
| `client_request` | object | Contains `request_id` + `command` — code the server asks the client to run. |

**Frame patterns by scenario:**

*Non-stream reply:*
```json
{"session_id": "<uuid>", "output": "Hello!", "done": true}
```

*Stream reply (multiple frames):*
```json
{"session_id": "<uuid>", "delta": "Hel"}
{"session_id": "<uuid>", "delta": "lo!"}
{"session_id": "<uuid>", "done": true}
```

*Error:*
```json
{"session_id": "<uuid>", "error": "something went wrong", "done": true}
```

*Tool call lifecycle (status: "start" → "ok" or "err"):*
```json
{"session_id": "<uuid>", "event": "tool", "tool": "read_file", "status": "start", "args": {"path": "README.md"}, "exec_snippet": "read_file 'README.md'"}
{"session_id": "<uuid>", "event": "tool", "tool": "read_file", "status": "ok",   "args": {"path": "README.md"}, "exec_snippet": "read_file 'README.md'", "data": {"result": "..."}}
```

*Token usage (after every LLM call):*
```json
{"session_id": "<uuid>", "event": "meta", "data": {"prompt_tokens": 1234, "completion_tokens": 56, "total_tokens": 1290, "duration_ns": 1234567890, "cost": 0.0000950625, "total_cost": 0.000190125}}
```

The `duration_ns` field reports the wall-clock time of the LLM call in nanoseconds.
It is available on every assistant message's `usage` field.
Use `completion_tokens / (duration_ns / 1e9)` to compute tokens/second throughput.

*End-of-turn session stats (before every `done: true` frame):*
```json
{"session_id": "<uuid>", "event": "meta", "data": {"total_tokens": 1290, "total_cost": 0.000190125, "message_count": 5, "estimated_context_tokens": 52000, "model": "deepseek"}}
```

This meta event is emitted **before** the final `done: true` frame at the end of every turn. It provides consolidated session statistics that clients can use to display updated totals after each interaction. The fields are:
- `total_tokens` — accumulated token usage across the entire session
- `total_cost` — accumulated monetary cost in USD
- `message_count` — total messages in the session
- `estimated_context_tokens` — last estimated context size in tokens
- `model` — the model used for this turn

*Estimated context size (before every LLM call):*
```json
{"session_id": "<uuid>", "event": "meta", "data": {"estimated_context_tokens": 52000}}
```

*Client request (server asks client to run code — Neovim Lua):*
```json
{"session_id": "<uuid>", "event": "client_request", "client_request": {"request_id": "req-1", "command": "return vim.api.nvim_buf_get_name(0)"}}
```

*Cancel acknowledgment:*
```json
{"session_id": "<uuid>", "event": "cancel", "done": true, "data": {"cancelled": true}}
```

*Session list (response to `list_sessions` or `session_list` command):*
```json
{"event": "command_result", "cmd": "session_list", "data": {
  "sessions": [
    {
      "id": "<uuid>",
      "short_id": "a1b2c3d4",
      "name": "Bug hunt",
      "message_count": 42,
      "current": true,
      "in_flight": false,
      "client_cwd": "/home/user/project",
      "created": "2025-01-15T10:30:00Z",
      "updated_at": "2025-01-15T14:22:00Z"
    }
  ]
}}
```

*Session fetch event (includes message history for UI reload):*
```json
{
  "session_id": "<uuid>",
  "event": "get_session",
  "data": {
    "session": {"id": "<uuid>", "short_id": "a1b2c3d4", "name": "Bug hunt", "message_count": 42, "model": "deepseek", "total_tokens": 5000, "estimated_context_tokens": 52000},
    "messages": [
      {"role": "user", "content": "What is a segmentation fault?"},
      {"role": "assistant", "content": "A segmentation fault is...", "usage": {"PromptTokens": 50, "CompletionTokens": 42, "TotalTokens": 92, "duration_ns": 1234567890}}
    ]
  }
}
```

Each assistant message carries a `usage` field with token counts and `duration_ns` — the
wall-clock time of the LLM call in nanoseconds. Duration is zero for messages stored before
this metric was introduced. Use `completion_tokens / (duration_ns / 1e9)` to compute
tokens/second throughput.
```

*Session created event:*
```json
{"session_id": "<uuid>", "event": "session_create", "data": {"session": {"id": "<uuid>", "short_id": "...", "name": "", "message_count": 0, "model": "deepseek", "total_tokens": 0, "estimated_context_tokens": 0}}}
```

*Session renamed event:*
```json
{"session_id": "<uuid>", "event": "session_renamed", "data": {"session_id": "<uuid>", "old_name": "bug", "name": "Bug investigation"}}
```

---

### Client Implementation Checklist

- [ ] **Connect** to `ws://<host>:<port>/ws`.
- [ ] **Send `{"type":"list_sessions"}`** on startup to populate the session picker.
- [ ] **Handle `done: true`** — the engine is not ready for new input until this frame arrives.
- [ ] **Render tool events** inline — `status: "start"` shows a spinner, `"ok"` renders the result, `"err"` renders the error.
- [ ] **Handle `client_request`** — if your client supports Neovim, evaluate the Lua and reply with `{"type":"response","request_id":"...","result":...}`. If not, reply with `{"type":"response","request_id":"...","error":"unsupported"}` so the tool doesn't hang.
- [ ] **Handle `get_session`** — reload the entire message view from `data.messages`.
- [ ] **Handle `command_result`** — parse `data` according to `cmd` (e.g. `session_list`, `tool_list`, `model_list`).
- [ ] **Handle `session_renamed`** — update the session name in the sidebar without reloading.
- [ ] **Send `cwd`** (via `cwd_set` command or per-request `cwd` field) on startup and when the user changes directory.
- [ ] **Send `cancel`** when the user hits Escape to abort a long turn.
- [ ] **Handle `error` frames** — show the error and consider the turn complete.
- [ ] **Stream interleaving** — frames can arrive in any order during streaming. Always buffer `delta` text; tool/meta events are informational and should be rendered at the stream position they occurred.

---

### Key Architectural Patterns

1. **Session Autonomy** — Sessions live in the store, not in connections. A turn runs on `context.Background()` so it survives client disconnect. Reconnecting clients subscribe to the ongoing turn via `TurnTracker`.
2. **Cooperative Streaming** — Stream tokens by default; if the model requests tools mid-stream, execute them, then stream the next response.
3. **Client/Response Loop** — `vim_run_command` sends a `client_request` frame, blocks on `ResponseReader`, and the client replies asynchronously over the same WebSocket.
4. **Model Switching at Runtime** — `/model <name>` or `model_switch` command changes the active model per-session, persisted in the store.
5. **LLM-Driven Context Compaction** — When estimated context exceeds `CompactSoftLimit` (default 200K), [N] message indices are injected and the LLM is prompted to call `context_compactify`. Compacted ranges become `[Compacted N messages: …]` markers. Full session data is never modified.
6. **Unified Command Path** — All session operations (create, fetch, delete, CWD, tools, models) go through the same JSON `command` dispatch in `CommandProcessor.ExecuteJSON`. Text-based slash commands are **not parsed server-side** — clients (or gateway-specific `parseSlashCommand` functions like the one in the Telegram gateway) convert them to JSON commands before sending.

---

## Extending

| Need | Interface/Component |
|------|-------------------|
| New LLM provider | `agent.LLMAdapter` |
| Persistent session store | `agent.SessionStore` |
| New tool | `agent.Tool` + `ToolRegistry.Register` |
| New transport | Implement a Gateway |
| Middleware/guardrails | Engine `Hooks` in `EngineConfig` |

---

## Testing Conventions

- Strong TDD approach (see `prompts.md`)
- Unit tests per component (`*_test.go` alongside each source file)
- Integration tests for the full engine loop (`engine_test.go`, `engine_iteration_test.go`, `conversation_test.go`, `conversation_mid_turn_test.go`)
- Turn runner tests (`turn_runner_test.go`)
- Streaming + tool fallback tests (`gateways/stream_tool_fallback_test.go`)
- Command processor tests (`commands/command_test.go`)
- Gateway reconnection and session fetch tests
- Concurrent Vim tool race tests (`gateways/vim_tool_race_test.go`)

---

## Workflow Rules

- **Always ask before implementing**: When the user gives you a task, first write down your intentions (what you plan to change, which files, and how), then ask for confirmation before writing any code. Do not jump straight into implementation.
- **TDD first**: When fixing a bug, first write a reproducing test, run it to ensure the bug is present. Only then fix it.

## Coding Conventions

- **Language**: Go (no CGO, pure Go SQLite via `modernc.org/sqlite`)
- **Error signaling**: Tools return recoverable errors as `"Error: ..."` plain text (model can self-correct); unrecoverable errors return a Go `error` which gets serialised as `{"error": "..."}`
- **Tool output convention (Plain Text = Default)**: All tools speak human-readable plain text, not JSON.
  - **Success**: Return minimal, self-explanatory text: `"Written 42 bytes to /tmp/foo.txt"`, not `{"bytes":42,"path":"/tmp/foo.txt"}`.
  - **Errors**: Return `"Error: <description>"` (model can self-correct); unrecoverable Go errors use `{"error": "..."}` envelope.
  - **Large output must not bloat context**: If output exceeds a reasonable threshold (e.g. 1 KB for shell, 200 KB for file reads), write it to a tempfile and return a summary with the file path so the LLM can `read_file` or `search` it only if needed.
  - **Truncation with affordance**: When truncating, always include a visible marker (`[TRUNCATED: N bytes omitted]`) and a summary of what was omitted so the LLM can decide whether to fetch more.
  - **Exceptions**: Tools with inherently multi-field or structured results may return JSON (`search`, `http_get`, `shell`), but even they should keep the structure flat and human-scanable.
- **Thread safety**: `Session` protected by `sync.RWMutex`; hooks serialised under a mutex in Conversation.
- **Logging**: `slog.Logger` throughout, passed via `EngineConfig`.
- **Configuration**: JSON model config with `${env: VAR_NAME}` placeholder substitution for credentials.
