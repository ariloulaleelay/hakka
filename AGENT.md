---
---
# Hakka — LLM Agent Core Framework

## Project Description

Hakka is a **minimal, modular, extensible LLM agent core framework** written in Go. It provides an orchestration engine that drives the LLM ↔ tool iteration loop, with pluggable model providers, persistent sessions, and transport-agnostic gateways (TCP, WebSocket, Telegram). It ships with a first-class Neovim plugin (`nvim/hakka.nvim`) that turns Neovim into an interactive agent IDE.

**Key design principles:**

- **Minimal Core, Maximum Extensibility** — Every component (LLM provider, persistence, tool, transport) is defined via Go interfaces.
- **Transport Agnosticism** — The engine is completely isolated from how the user connects (TCP, WebSocket, Telegram, Neovim).
- **Agentic Autonomy** — The framework natively handles the full LLM ↔ tool iteration cycle with concurrent tool execution.
- **Rich Client Observability** — Structured lifecycle hooks (tool call start/success/failure) let clients like Neovim render agent actions interactively.

---

## Brief Architecture

```
                         ┌─────────────────────────────────────┐
                         │           GATEWAYS                  │
                         │  TCP · WebSocket · Telegram         │
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
| **Session** | `agent/session.go` | Append-only conversation history, system prompt, thread-safe state bag (`map[string]any`), model binding, token tracking |
| **SessionStore** | `agent/store.go` | Pluggable persistence — `MemoryStore` (in-process) or `sqlite.Store` (disk-backed via `modernc.org/sqlite`, CGO-free) |
| **SessionManager** | `agent/manager.go` | Wraps a store with `GetOrCreate`/`Save`/`Drop` helpers |
| **SessionView** | `agent/session_view.go` | Role-specific session interfaces (ISP) — `SessionHistory`, `SessionToolAuth`, `SessionModelBinding`, `SessionIdentity` |
| **LLMAdapter** | `agent/adapter.go` | Interface: `Complete()` + `Stream()`. Implementations in `agent/adapters/` for OpenAI, Anthropic, Gemini |
| **Registry** | `agent/registry.go` | Named collection of adapters with a default selection |
| **Router** | `agent/router.go` | Decides which adapter serves a given session based on the session's model binding. Single owner of the `Model` field |
| **Tool / ToolRegistry** | `agent/tool.go` | Tool schema + handler + optional timeout + exec snippet. Concurrent-safe registry |
| **Conversation** | `agent/conversation.go` | High-level public API for gateways — session lifecycle, turn dispatch, auto-rename. Delegates the LLM↔tool loop internals to `turnRunner` |
| **TurnRunner** | `agent/turn_runner.go` | Drives the LLM ↔ tool iteration loop (extracted from Conversation per SRP). Owns the step function, compaction limit resolution, and compactify schema augmentation |
| **ToolExecutor** | `agent/tool_executor.go` | Concurrent tool execution with fan-out, event hooks, and result appending (extracted from Conversation per SRP) |
| **Compact** | `agent/compact.go` | LLM-driven context compaction — when estimated context exceeds the soft limit, [N] message indices and a warning prompt the LLM to call `context_compactify` to compress unneeded ranges; controlled per-session via `Session.CompactSoftLimit` |
| **StreamSession** | `agent/stream_session.go` | Cooperative streaming — streams text tokens, but if the model requests tools, aborts the stream and falls back to the tool loop |
| **CommandProcessor** | `agent/commands/` | Slash-commands (`/help`, `/model`, `/session`, `/tools`) without any LLM dependency. Split into `command_processor.go` (dispatcher), `model_commands.go`, `session_commands.go`, `tool_commands.go` |
| **AutoRenamer** | `agent/auto_renamer.go` | LLM-driven session naming logic (extracted from Conversation per SRP) |
| **Truncate** | `agent/truncate.go` | UTF-8-safe string truncation utility with `...` suffix |
| **ProcessManager** | `agent/tools/process.go` | Goroutine-safe subprocess registry; manages spawn, interact, kill, and lifecycle of child processes with ring-buffered stdout+stderr output |
| **Gateway** | `agent/gateways/` | Transport layer — TCP (`tcp.go`), WebSocket (`websocket.go`), Telegram (`telegram.go`). Wire framing in `frame.go`. Each wraps `Conversation` + `StreamSession` + `CommandProcessor` |
| **ResponseReader** | `agent/gateways/response_reader.go` | Async response awaiting for client-initated tool calls (e.g. Neovim `vim_run_command`) |
| **TurnTracker** | `agent/gateways/turn_tracker.go` | Serialises concurrent turns per session — prevents race conditions when multiple inputs arrive simultaneously |
| **Hooks** | `agent/engine.go` | Lifecycle callbacks: `OnToolCall`, `OnToolResult`, `OnLLMResponse`, `OnError` — serialised under a mutex for safe transport use |
| **EngineEvent types** | `agent/event/event.go` | Typed event structs (`ToolCallStarted`, `ToolCallFinished`, `UsageReported`, `TextDelta`, etc.) emitted by the engine and consumed by transports |
| **EngineChannelWriter** | `agent/event/engine_writer.go` | Funnels tool-to-client requests into the engine's event channel, serialising all outbound writes without explicit locking |
| **Format** | `agent/format/md2tg.go` | Markdown-to-Telegram-HTML conversion for the Telegram gateway |
| **Batch** | `batch/batch.go` | Reusable API for running Hakka in batch mode — a single autonomous task with no interactive transport |

### Built-in Tools (`agent/tools/`)

| Tool | Description |
|---|---|
| `read_file` | Read UTF-8 text files; truncates at 200K bytes with `[TRUNCATED <n> bytes]` marker |
| `write_file` | Create/overwrite files; creates parent directories automatically |
| `edit_file` | Literal string search-and-replace (first or all occurrences) |
| `list_dir` | List directory entries (dirs as `name/`, files as `name\t<size>`) |
| `shell` | Execute `sh -c` commands; short output inlined, large output saved to tempfiles |
| `http_get` | HTTP GET with headers, returns status, headers, and body. HTML content is automatically converted to Markdown for easier LLM reading. |
| `feedback` | Submit anonymous feedback (feature request or bug report) with a unique anonymous ID |
| `random` | Generate a random integer between min_value and max_value (inclusive). |
| `search` | ripgrep recursive search with file:line:col output |
| `vim_run_command` | Execute Lua in the user's Neovim instance (requires Neovim client) |
| `vim_list_buffers` | List open Neovim buffers with numbers, names, and filetypes |
| `vim_read_buffer` | Read a Neovim buffer by number as numbered lines |
| `spawn_process` | Start a subprocess and return its unique ID for subsequent interaction |
| `interact_process` | Send input to a running process and/or read its pending stdout+stderr output |
| `kill_process` | Terminate a running process (SIGTERM by default, SIGKILL available) |
| `list_processes` | List all spawned processes with ID, PID, state, uptime, and command |
| `session_list` | List all sessions with metadata (name, messages, model, created) |
| `session_rename` | Rename a session by ID or unique prefix |
| `session_info` | Show detailed session info (messages, tokens, model, first/last messages) |
| `session_read` | Read messages from a session, with optional role filter and limit |
| `session_search` | Search for text across all sessions in the current namespace |
| `session_summarize` | Summarise a session's conversation (LLM-based or heuristic) |
| `session_ask_question` | Ask an LLM a question about a session's conversation history (virtual, no persistence) |
| `session_delete` | Delete a session by ID or unique prefix |
| `session_create` | Create a new empty session |
| `context_compactify` | Compact message ranges to free context space (meta-tool; only appears when context exceeds `CompactSoftLimit`) |
| `show_tool` | Show detailed info about a specific tool (name, description, parameters, tags). Also enables the tool |
| `show_tool` | Show detailed info about a specific tool (name, description, parameters, tags) |

Tools tagged `"tool"` (`show_tool`) is **pre-enabled** in every new session so the LLM can always discover and enable tools at runtime. `show_tool` also automatically enables the inspected tool, making it callable via the API "tools" section. The system prompt lists all allowed (non-denied) tools with their enabled/disabled status, so the LLM always knows what's available.

Session tools are scoped to the per-gateway namespace via Go context (see `agent/event/event.go` — `ContextWithNamespace` / `NamespaceFromContext`). A TCP client can only see `"tcp"` sessions; a Telegram chat only sees its own `"tg:<chat_id>"` sessions.

### Key Architectural Patterns

1. **Cooperative Resumption** — Stream by default; if the model requests tools mid-stream, abort streaming, execute tools, deliver final reply as a single chunk.
2. **Hook Serialisation** — Tool hooks are serialised under a mutex so transport writers don't need to be concurrency-safe.
3. **Client/Response (Vim Tool)** — `vim_run_command` sends a request frame to the client, blocks on a `ResponseReader`, and the client replies asynchronously over the same connection.
4. **Model Switching at Runtime** — Slash commands `/model <name>` change the active model per-session via the Router, persisted in the session store.
5. **LLM-Driven Context Compaction** — When estimated context tokens exceed `Session.CompactSoftLimit` (default 150K), the engine injects `[N]` message indices and a system warning, prompting the LLM to call `context_compactify`. The LLM decides which message ranges to compact. Past `context_compactify` calls are scanned and applied on every context build — compacted ranges become `[Compacted N messages: …]` markers, and the compactify call/result messages themselves are filtered from the view. Full session data is never modified. Controlled per-session via `Session.CompactSoftLimit` (default 150000).

### Wire Protocol

**Request** (TCP newline-JSON or WebSocket frame):
```json
{ "session_id": "<uuid>", "input": "...", "stream": true }
```

**Non-stream response**:
```json
{ "session_id": "...", "output": "...", "done": true }
```

**Stream response** (deltas + termination):
```json
{ "session_id": "...", "delta": "chunk" }
...
{ "session_id": "...", "done": true }
```

**Observability events**:
```json
{ "session_id": "...", "event": "tool", "data": {"tool": "read_file", "status": "start"} }
```

### Testing Conventions

- Strong TDD approach (see `prompts.md`)
- Unit tests per component (`*_test.go` alongside each source file)
- Integration tests for the full engine loop (`engine_test.go`, `engine_iteration_test.go`, `conversation_test.go`, `conversation_mid_turn_test.go`)
- Turn runner tests (`turn_runner_test.go`)
- Streaming + tool fallback tests (`gateways/session_loop_stream_vim_test.go`, `gateways/stream_tool_fallback_test.go`)
- Command processor tests (`commands/command_test.go`)
- Parallel tool execution tests (`gateways/tcp_parallel_tools_test.go`)
- Gateway reconnection and session switch tests (`gateways/reconnect_test.go`, `gateways/reconnect_switch_test.go`, `gateways/session_switch_test.go`)
- Concurrent Vim tool race tests (`gateways/vim_tool_race_test.go`)

---

## Workflow Rules

- **Always ask before implementing**: When the user gives you a task, first write down your intentions (what you plan to change, which files, and how), then ask for confirmation before writing any code. Do not jump straight into implementation.
- **TDD first**: When fixing bug, first write reproducing test, run it to ensure bug is here. Only then you can fix it.

## Coding Conventions

- **Language**: Go (no CGO, pure Go SQLite via `modernc.org/sqlite`)
- **Error signaling**: Tools return recoverable errors as `"Error: ..."` plain text (model can self-correct); unrecoverable errors return a Go `error` which gets serialised as `{"error": "..."}`
- **Tool output convention (Plain Text = Default)**: All tools speak human-readable plain text, not JSON.
  - **Success**: Return minimal, self-explanatory text: `"Written 42 bytes to /tmp/foo.txt"`, not `{"bytes":42,"path":"/tmp/foo.txt"}`.
  - **Errors**: Return `"Error: <description>"` (model can self-correct); unrecoverable Go errors use `{"error": "..."}` envelope.
  - **Large output must not bloat context**: If output exceeds a reasonable threshold (e.g. 1 KB for shell, 200 KB for file reads), write it to a tempfile and return a summary with the file path so the LLM can `read_file` or `search` it only if needed.
  - **Truncation with affordance**: When truncating, always include a visible marker (`[TRUNCATED: N bytes omitted]`) and a summary of what was omitted so the LLM can decide whether to fetch more.
  - **Exceptions**: Tools with inherently multi-field or structured results may return JSON (`search`, `http_get`, `shell`), but even they should keep the structure flat and human-scanable.
- **Thread safety**: `Session.Messages` protected by `sync.Mutex`; `Session.State` accessed only through the session; hooks serialised under a mutex in Conversation
- **Logging**: `slog.Logger` throughout, passed via `EngineConfig`
- **Configuration**: JSON model config with `${env: VAR_NAME}` placeholder substitution for credentials


