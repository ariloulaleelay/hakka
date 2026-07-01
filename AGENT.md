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

Hakka uses a **WebSocket JSON API**. Every message in both
directions is a single JSON object with a mandatory `type` field as
the sole discriminant.

Key points:
- LLM content is always in a field called `text` — whether streaming (`delta`) or final (`done`)
- No `protocol_version` field — versioning is implicit
- `type:"req"` frames have flat `request_id`/`command` fields (no nested `client_request`)
- `type:"welcome"` has `sessions` at the top level (not inside `data`)
- `type:"session"` has `session`/`messages` at the top level (not inside `data`)
- The `ContextEstimated` event is not emitted on the wire (estimated context is in the `usage` frame)

### Connection & Session Lifecycle (for client authors)

A typical client follows this sequence:

1. **Connect** — open a WebSocket to `/ws`.
2. **Receive welcome** — the server immediately sends a `"type":"welcome"`
   frame with all sessions and their `in_flight` status. The client is
   auto-subscribed to the best session (one with an active turn, or the
   most recent).
3. **Pick or create a session** — if no session exists, send
   `{"type":"cmd","command":{"cmd":"session_create"}}`. If you want a
   different session, send `{"type":"cmd","command":{"cmd":"get_session","params":{"id":"..."}}}`.
4. **Set working directory** — `{"type":"cmd","command":{"cmd":"cwd_set","params":{"cwd":"/path"}}}`.
5. **Enable tools** — `{"type":"cmd","command":{"cmd":"tool_allow","params":{"name":"#all"}}}`.
6. **Send input** — `{"type":"chat","session_id":"...","input":"Hello!","stream":true}`.
7. **Receive responses** — `delta` (text chunks), `tool` (tool lifecycle),
   `usage` (token counts), and finally `type:"done"`.
8. **Fetch a session** — `{"type":"cmd","command":{"cmd":"get_session","params":{"id":"..."}}}`.

**Key rules for client authors:**

- Always include `session_id` on every request once you have one.
- `stream: true` gives incremental `type:"delta"` frames with `text` field;
  `stream: false` gives a single `type:"done"` frame with `text` field.
- The `type:"done"` frame **always** arrives — even on errors, even after
  cancel. It is your signal that the turn is complete.
- Tool events (`type:"tool"`) and usage events (`type:"usage"`) may arrive
  interleaved with deltas. Each tool event carries a unique `id` field
  that correlates `status:"start"` with `status:"ok"`/`status:"err"`.
- If the LLM calls `vim_run_command`, you will receive a `type:"req"` event
  with flat `request_id` and `command` fields. You **must** reply with
  `{"type":"resp","request_id":"...","result":...}` or the tool will block
  until timeout.
- Cancel an in-flight turn by sending `{"type":"cancel","session_id":"..."}`.
  The server replies with `{"type":"done","cancelled":true}`.
- Set CWD before sending substantive input via `cwd_set` command.

See `protocol.md` for the complete frame reference.
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
