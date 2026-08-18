---
---
---
# Hakka — LLM Agent Core Framework

## Project Description

Hakka is a **minimal, modular, extensible LLM agent core framework** written in Go. It provides an orchestration engine that drives the LLM ↔ tool iteration loop, with pluggable model providers, persistent sessions, and transport-agnostic gateways (WebSocket, Telegram). It pairs with a dedicated [hakka.nvim](https://github.com/ariloulaleelay/hakka.nvim) plugin that turns Neovim into an interactive agent IDE.

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
                         │    commands/ (slash-commands)       │
                         │    NamespaceHub (broadcast)         │
                         └──────────┬──────────────────────────┘
                                    │
              ┌─────────────────────┼─────────────────────┐
              ▼                     ▼                     ▼
   ┌──────────────────┐   ┌──────────────┐   ┌──────────────────────────┐
   │  ADAPTERS        │   │ SESSION      │   │  TOOLS                   │
   │  OpenAI          │   │ STORE        │   │  read_file               │
   │  Anthropic       │   │  Memory      │   │  write_file              │
   │  Gemini          │   │  SQLite      │   │  edit_file               │
   │  DeepSeek        │   │  PostgreSQL  │   │  list_dir                │
   └──────────────────┘   │  PostgreSQL  │   │  list_dir                │
                          └──────────────┘   │  shell                   │
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
| **SessionStore** | `agent/store.go` | Pluggable persistence — `MemoryStore` (in-process), SQLite (disk-backed via `modernc.org/sqlite`, CGO-free), or PostgreSQL (via `lib/pq`) |
| **SessionManager** | `agent/manager.go` | Wraps a store with `GetOrCreate`/`Save`/`Drop` helpers |
| **SessionView** | `agent/session_view.go` | Role-specific session interfaces (ISP) — `SessionHistory`, `SessionToolAuth`, `SessionModelBinding`, `SessionIdentity` |
| **LLMAdapter** | `agent/adapter.go` | Interface: `Complete()` + `Stream()`. Implementations in `agent/adapters/` for OpenAI, Anthropic, Gemini, DeepSeek |
| **Registry** | `agent/registry.go` | Named collection of adapters with a default selection |
| **Router** | `agent/router.go` | Decides which adapter serves a given session based on the session's model binding. Single owner of the `Model` field |
| **Tool / ToolRegistry** | `agent/tool.go` | Tool schema + handler + optional timeout + exec snippet. Concurrent-safe registry |
| **Conversation** | `agent/conversation.go` | High-level public API for gateways — session lifecycle, turn dispatch, auto-rename. Delegates the LLM↔tool loop internals to `turnRunner` |
| **TurnRunner** | `agent/turn_runner.go` | Drives the LLM ↔ tool iteration loop (extracted from Conversation per SRP). Owns the step function, compaction limit resolution, and compactify schema augmentation |
| **ToolExecutor** | `agent/tool_executor.go` | Concurrent tool execution with fan-out, event hooks, and result appending |
| **Compact** | `agent/compact.go` | LLM-driven context compaction — when estimated context exceeds the soft limit, [N] message indices and a warning prompt the LLM to call `context_compactify` |
| **CommandProcessor** | `agent/commands/` | Structured JSON-only command handler (`ExecuteJSON`) — no text slash-command parsing. Delegates to ModelCommands, SessionCommands, ToolCommands. Clients map slash-commands to JSON commands locally. |
| **AutoRenamer** | `agent/auto_renamer.go` | LLM-driven session naming logic |
| **ProcessManager** | `agent/tools/process.go` | Goroutine-safe subprocess registry with ring-buffered stdout+stderr |
| **Gateway** | `agent/gateways/` | Transport layer — WebSocket (`websocket.go`), Telegram (`telegram.go`). Wire framing in `frame.go`. |
| **ResponseReader** | `agent/gateways/response_reader.go` | Async response awaiting for client-initiated tool calls (e.g. Neovim `vim_run_command`) |
| **TurnTracker** | `agent/gateways/turn_tracker.go` | Manages active turns per session — cancellation, in_flight status. All turn events are broadcast to every connected client via the `NamespaceHub`; no per-client subscription lists |
| **NamespaceHub** | `agent/gateways/namespace_hub.go` | Per-namespace event broadcast hub. All connected clients (WS, webfront) register their writers here. All turn events and session lifecycle events are broadcast to every subscriber in the namespace. Owns the namespace's shared `turnTracker`. |
| **Hooks** | `agent/engine.go` | Lifecycle callbacks: `OnToolCall`, `OnToolResult`, `OnLLMResponse`, `OnError` |
| **EngineEvent types** | `agent/event/event.go` | Typed event structs (`ToolCallStarted`, `ToolCallFinished`, `UsageReported`, `TextDelta`, etc.) |
| **EngineChannelWriter** | `agent/event/engine_writer.go` | Serialises tool-to-client requests through the engine event channel |
| **Format** | `agent/format/md2tg.go` | Markdown-to-Telegram-HTML conversion for the Telegram gateway |
| **Batch** | `batch/batch.go` | Reusable API for running Hakka in batch mode — autonomous tasks with no interactive transport |

### Session Storage

Sessions are persisted via the `SessionStore` interface (`agent/store.go`). Three backends are available:

| Backend | Connection | Description |
|---|---|---|
| Memory | (default) | In-process map, goroutine-safe. Used when no `--db` flag is given. |
| SQLite | `--db /path/to/hakka.db` or `--db sqlite:/path/to/hakka.db` | CGO-free via `modernc.org/sqlite`. Automatic migration from the old single-table schema. |
| PostgreSQL | `--db postgres://user:pass@host/dbname?sslmode=disable` | Via `lib/pq`. Full `database/sql` driver. |

Messages are stored in a dedicated `messages` table (split from the `sessions` metadata table)
with a foreign key (`session_ns`, `session_id`) referencing `sessions(namespace, id)` with
`ON DELETE CASCADE`. This replaces the previous single-JSON-blob approach, enabling future
incremental message operations.

### Built-in Tools (`agent/tools/`)

| Tool | Description |
|---|---|
| `read_file` | Read UTF-8 text files; supports offset+limit for windowed reading, truncates with `[TRUNCATED ...]` marker |
| `write_file` | Create/overwrite files; creates parent directories automatically; auto-checkpoints before overwrite |
| `edit_file` | Literal string search-and-replace (first or all occurrences); auto-checkpoints before edit |
| `list_dir` | List directory entries (dirs as `name/`, files as `name\t<size>`) |
| `shell` | Execute `sh -c` commands; short output inlined, large output saved to tempfiles, auto-checkpoints for likely modified files |
| `rollback` | Restore a file to its pre-modification state from a checkpoint ID |
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

### Skill Tools

| Tool | Description |
|---|---|
| `search_skills` | Search this session's skill registry by name, description, or tags. Includes full metadata (license, compatibility, allowed tools) — no inspect step needed |
| `import_skill` | Register skill(s) from a path into THIS session's registry — SKILL.md file, single skill dir, or registry dir. Never loads into session; never visible to other sessions |
| `load_skill` | Load a registered skill into the session — its content becomes part of the system prompt on every turn |
| `unload_skill` | Remove a loaded skill from the session to free context |

Skills are reusable instructions/knowledge that teach the agent HOW to do something. They follow the [Agent Skills specification](https://agentskills.io): a skill is a directory containing a `SKILL.md` file with YAML frontmatter (`name`, `description`, `license`, `compatibility`, `metadata`, `allowed-tools`) and optional `scripts/`, `references/`, `assets/` subdirectories.

**Skills are session-bound** — there is no server-global skill registry:

- Each session owns its own `SkillRegistry`, built lazily from its persisted imports plus the `${cwd}/skills` directory (auto-imported when it exists).
- `import_skill` registers skills into the current session only and persists the SKILL.md paths with the session, so imports survive restarts and follow forks.
- `load_skill`/`unload_skill` maintain the session's `ActiveSkills` list (persisted with the session). When loaded, skill content is injected as additional `RoleSystem` messages after the base system prompt.
- Forked/subagent sessions inherit the parent's skill state (imported paths and active skills).

A skill **registry** is a directory containing multiple skill subdirectories:

```
skills/
├── pdf-processing/
│   ├── SKILL.md
│   └── references/
├── data-analysis/
│   ├── SKILL.md
│   └── scripts/
└── code-review/
    └── SKILL.md
```

A `${cwd}/skills` directory in the session's working directory is picked up automatically. There is no backward compatibility with flat `.md` files — only the spec format is supported.

The `context_compactify` warning also suggests using `unload_skill` to free context when the soft limit is exceeded.

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
- `type:"session"` has `session`/`events` at the top level (not inside `data`), no `messages` field
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
6. **Send input** — `{"type":"chat","session_id":"...","input":"Hello!"}`.
7. **Receive responses** — `delta` (text chunks), `tool` (tool lifecycle),
   `usage` (token counts), and finally `type:"done"`.
8. **Fetch a session** — `{"type":"cmd","command":{"cmd":"get_session","params":{"id":"..."}}}`.

**Key rules for client authors:**

- Always include `session_id` on every request once you have one.
- **All events are broadcast to all clients in the namespace.** Filter by
  `session_id` to show only the session(s) you are viewing.
- Streaming is adapter-controlled: the server emits `type:"delta"` frames
  when the adapter supports streaming, or just a `type:"done"` frame with
  the full text. Clients must handle both paths.
- The `type:"done"` frame **always** arrives — even on errors, even after
  cancel. It is your signal that the turn is complete.
- Tool events (`type:"tool"`) and usage events (`type:"usage"`) may arrive
  interleaved with deltas. Each tool event carries a unique `id` field
  that correlates `status:"start"` with `status:"ok"`/`status:"err"`.
- If the LLM calls `vim_run_command`, you will receive a `type:"req"` event
  with flat `request_id` and `command` fields — broadcast to ALL clients.
  You **must** reply with
  `{"type":"resp","request_id":"...","result":...}` or the tool will block
  until timeout. First responder wins.
- Cancel an in-flight turn by sending `{"type":"cancel","session_id":"..."}`.
  The server replies with `{"type":"done","cancelled":true}` immediately,
  followed by a second `done` with `stats` when the turn terminates.
- Set CWD before sending substantive input via `cwd_set` command.

See `protocol.md` for the complete frame reference.
## Extending

| Need | Interface/Component |
|------|-------------------|
| New LLM provider | `agent.LLMAdapter` |
| Persistent session store | `agent.SessionStore` |
| New tool | `agent.Tool` + `ToolRegistry.Register` |
| New skill | `agent.Skill` + per-session `SkillRegistry` (see Skill Tools — skills are session-bound) |
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

- **Language**: Go
- **Error signaling**: Tools return recoverable errors as `"Error: ..."` plain text (model can self-correct); unrecoverable errors return a Go `error` which gets serialised as `{"error": "..."}`
- **Tool output convention (Plain Text = Default)**: All tools speak human-readable plain text, not JSON.
  - **Success**: Return minimal, self-explanatory text: `"Written 42 bytes"`, not `{"bytes":42,"path":"/tmp/foo.txt"}`.
  - **Errors**: Return `"Error: <description>"` (model can self-correct); unrecoverable Go errors use `{"error": "..."}` envelope.
  - **Large output must not bloat context**: If output exceeds a reasonable threshold (e.g. 1 KB for shell, 200 KB for file reads), write it to a tempfile and return a summary with the file path so the LLM can `read_file` or `search` it only if needed.
  - **Truncation with affordance**: When truncating, always include a visible marker (`[TRUNCATED: N bytes omitted]`) and a summary of what was omitted so the LLM can decide whether to fetch more.
  - **Exceptions**: Tools with inherently multi-field or structured results may return JSON (`search`, `http_get`, `shell`), but even they should keep the structure flat and human-scanable.
- **Thread safety**: `Session` protected by `sync.RWMutex`; hooks serialised under a mutex in Conversation.
- **Logging**: `slog.Logger` throughout, passed via `EngineConfig`.
- **Configuration**: JSON model config with `${env: VAR_NAME}` placeholder substitution for credentials.
