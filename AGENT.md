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
                         │    StreamSession (stream+fallback)  │
                         │    CommandProcessor (/commands)     │
                         └──────────┬──────────────────────────┘
                                    │
              ┌─────────────────────┼─────────────────────┐
              ▼                     ▼                     ▼
   ┌──────────────────┐   ┌──────────────┐   ┌──────────────────────┐
   │  ADAPTERS        │   │ SESSION      │   │  TOOLS               │
   │  OpenAI          │   │ STORE        │   │  read_file           │
   │  Anthropic       │   │  Memory      │   │  write_file          │
   │  Gemini          │   │  SQLite      │   │  edit_file           │
   └──────────────────┘   └──────────────┘   │  list_dir            │
                                             │  shell               │
                                             │  http_get            │
                                             │  search (ripgrep)    │
                                             │  vim_run_command     │
                                             └──────────────────────┘
```

### Core Components

| Component | File(s) | Role |
|---|---|---|
| **Session** | `agent/session.go` | Append-only conversation history, system prompt, thread-safe state bag (`map[string]any`), model binding, token tracking |
| **SessionStore** | `agent/store.go` | Pluggable persistence — `MemoryStore` (in-process) or `sqlite.Store` (disk-backed via `modernc.org/sqlite`, CGO-free) |
| **SessionManager** | `agent/manager.go` | Wraps a store with `GetOrCreate`/`Save`/`Drop` helpers |
| **LLMAdapter** | `agent/adapter.go` | Interface: `Complete()` + `Stream()`. Implementations in `agent/adapters/` for OpenAI, Anthropic, Gemini |
| **Registry** | `agent/registry.go` | Named collection of adapters with a default selection |
| **Router** | `agent/router.go` | Decides which adapter serves a given session based on the session's model binding. Single owner of the `Model` field |
| **Tool / ToolRegistry** | `agent/tool.go` | Tool schema + handler + optional timeout + exec snippet. Concurrent-safe registry |
| **Conversation** | `agent/conversation.go` | Drives the LLM ↔ tool loop — append user input, call LLM, execute tools concurrently, loop until final text or `MaxToolIterations` (default 128) |
| **StreamSession** | `agent/stream_session.go` | Cooperative streaming — streams text tokens, but if the model requests tools, aborts the stream and falls back to the tool loop |
| **CommandProcessor** | `agent/command_processor.go` | Slash-commands (`/help`, `/model`, `/session`, `/models`) without any LLM dependency |
| **Gateway** | `agent/gateways/` | Transport layer — TCP (newline-JSON), WebSocket, Telegram. Each wraps `Conversation` + `StreamSession` + `CommandProcessor` |
| **Hooks** | `agent/engine.go` | Lifecycle callbacks: `OnToolCall`, `OnToolResult`, `OnLLMResponse`, `OnError` — serialised under a mutex for safe transport use |

### Built-in Tools (`agent/tools/`)

| Tool | Description |
|---|---|
| `read_file` | Read UTF-8 text files; truncates at 200K bytes with `[TRUNCATED <n> bytes]` marker |
| `write_file` | Create/overwrite files; creates parent directories automatically |
| `edit_file` | Literal string search-and-replace (first or all occurrences) |
| `list_dir` | List directory entries (dirs as `name/`, files as `name\t<size>`) |
| `shell` | Execute `sh -c` commands; short output inlined, large output saved to tempfiles |
| `http_get` | HTTP GET with headers, returns status/headers/body |
| `search` | ripgrep recursive search with file:line:col output |
| `vim_run_command` | Execute Lua in the user's Neovim instance (requires Neovim client) |
| `session_list` | List all sessions with metadata (name, messages, model, created) |
| `session_rename` | Rename a session by ID or unique prefix |
| `session_info` | Show detailed session info (messages, tokens, model, first/last messages) |
| `session_read` | Read messages from a session, with optional role filter and limit |
| `session_search` | Search for text across all sessions in the current namespace |
| `session_summarize` | Summarise a session's conversation (LLM-based or heuristic) |
| `session_ask_question` | Ask an LLM a question about a session's conversation history (virtual, no persistence) |
| `session_delete` | Delete a session by ID or unique prefix |
| `session_create` | Create a new empty session |

Session tools are scoped to the per-gateway namespace via Go context (see `agent/event/event.go` — `ContextWithNamespace` / `NamespaceFromContext`). A TCP client can only see `"tcp"` sessions; a Telegram chat only sees its own `"tg:<chat_id>"` sessions.

### Key Architectural Patterns

1. **Cooperative Resumption** — Stream by default; if the model requests tools mid-stream, abort streaming, execute tools, deliver final reply as a single chunk.
2. **Hook Serialisation** — Tool hooks are serialised under a mutex so transport writers don't need to be concurrency-safe.
3. **Client/Response (Vim Tool)** — `vim_run_command` sends a request frame to the client, blocks on a `ResponseReader`, and the client replies asynchronously over the same connection.
4. **Model Switching at Runtime** — Slash commands `/model <name>` change the active model per-session via the Router, persisted in the session store.

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
- Integration tests for the full engine loop (`engine_test.go`, `engine_iteration_test.go`)
- Streaming + tool fallback tests (`session_loop_stream_vim_test.go`)
- Bug reproduction tests (`anthropic_bug_test.go`, `gemini_bug_test.go`)
- Parallel tool execution tests (`tcp_parallel_tools_test.go`)

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
