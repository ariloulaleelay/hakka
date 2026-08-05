# Hakka Wire Protocol

## Overview

Hakka uses a **WebSocket JSON API**. Every message in both directions is a
single JSON object with a mandatory `type` field as the sole discriminant.

---

## Frame Reference

### Inbound Frames (Client → Server)

#### `type:"chat"` — Send a user message for LLM processing

```json
{"type":"chat", "session_id":"<uuid>", "input":"..."}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"chat"` |
| `session_id` | ✓ | Session UUID (must be obtained via `session_create` or `get_session`) |
| `input` | ✓ | User message text |

The server always emits `type:"delta"` frames when the adapter supports
streaming; the adapter decides internally whether to stream. Clients
must always be prepared to receive both `delta` and `done` frames in
any turn.

#### `type:"cmd"` — Send a JSON command

```json
{"type":"cmd", "session_id":"<uuid>", "command":{"cmd":"session_info"}}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"cmd"` |
| `session_id` | | Session UUID (required for session-scoped commands) |
| `command` | ✓ | `{"cmd": "...", "params": {...}}` |

**Example: fork a session**

```json
{"type":"cmd", "command":{"cmd":"session_fork", "params":{"id":"<parent-id>", "fork_point":"<msg-id>"}}}
```

Response (via `type:"session"`):
```json
{
  "type": "session",
  "session_id": "<child-id>",
  "event": "session_create",
  "session": {"id": "<child-id>", "parent_id": "<parent-id>", "fork_point": "<msg-id>", ...}
}
```

#### `type:"resp"` — Respond to a server request

```json
{"type":"resp", "request_id":"req-1", "result":42}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"resp"` |
| `request_id` | ✓ | Matches the `request_id` from the `type:"req"` frame |
| `result` | | JSON result (optional, for success) |
| `error` | | Error message (optional, for failure) |

#### `type:"cancel"` — Cancel an in-flight turn

```json
{"type":"cancel", "session_id":"<uuid>"}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"cancel"` |
| `session_id` | ✓ | Session UUID to cancel |

---

### Outbound Frames (Server → Client)

#### `type:"welcome"` — Sent immediately on connect

```json
{
  "type": "welcome",
  "sessions": [
    {"id": "...", "short_id": "...", "name": "...", "model": "...", "message_count": 0, "in_flight": false}
  ]
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"welcome"` |
| `sessions` | ✓ | Array of session metadata (top level, not nested in `data`) |

Each session entry in `sessions`:

| Field | Description |
|-------|-------------|
| `id` | Full session ID |
| `short_id` | First 8 characters of ID |
| `name` | Human-readable session name (or `""`) |
| `model` | Active model name |
| `message_count` | Number of messages in session |
| `in_flight` | Whether a turn is currently running on this session |
| `parent_id` | Parent session ID (empty for root sessions) |
| `fork_point` | Message ID in parent at fork boundary (empty for root sessions) |
| `total_tokens` | Accumulated token usage across the session |
| `total_cost` | Accumulated monetary cost in USD |
| `estimated_context_tokens` | Last estimated context size in tokens |
| `client_cwd` | Working directory for the session |
| `compact_soft_limit` | Context compaction threshold (0 = off) |
| `created_at` | ISO 8601 creation timestamp |
| `updated_at` | ISO 8601 last-update timestamp |

#### `type:"delta"` — Streaming text chunk

```json
{"type":"delta", "session_id":"<uuid>", "id":"<msg-id>", "text":"chunk", "ts": 1700000000123}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"delta"` |
| `session_id` | ✓ | Session UUID |
| `id` | | Message ID of the assistant message being streamed |
| `text` | ✓ | Content chunk (may be partial word) |
| `ts` | | Unix timestamp in milliseconds when the event was generated |

Multiple `delta` frames are sent during streaming, one per chunk.

#### `type:"done"` — Turn complete

```json
{
  "type": "done",
  "session_id": "<uuid>",
  "id": "<msg-id>",
  "text": "final reply",
  "ts": 1700000000123,
  "stats": {
    "total_tokens": 1290,
    "total_cost": 0.00019,
    "message_count": 5,
    "estimated_context_tokens": 52000,
    "model": "deepseek"
  }
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"done"` |
| `session_id` | ✓ | Session UUID |
| `id` | | Message ID of the final assistant message |
| `text` | | Final assistant reply (omitted on cancellation) |
| `error` | | Error message if turn failed |
| `cancelled` | | `true` if turn was cancelled by client |
| `ts` | | Unix timestamp in milliseconds |
| `stats` | ✓ | End-of-turn statistics |

The `done` frame **always** arrives — even on errors, even after cancel.
It is the signal that the turn is complete.

#### `type:"tool"` — Tool lifecycle event

```json
{"type":"tool", "session_id":"<uuid>", "id":"call_1", "tool":"read_file", "status":"start", "args":{"path":"README.md"}, "snippet":"read_file 'README.md'", "ts": 1700000000123}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"tool"` |
| `session_id` | ✓ | Session UUID |
| `id` | ✓ | Unique tool call ID (correlates start ↔ ok/err) |
| `tool` | ✓ | Tool name |
| `status` | ✓ | One of `"start"`, `"ok"`, `"err"` |
| `args` | | Tool arguments (present on `"start"`) |
| `snippet` | | Human-readable execution summary |
| `result` | | Tool output (present on `"ok"`) |
| `error` | | Error message (present on `"err"`) |
| `ts` | | Unix timestamp in milliseconds |

Tool lifecycle:
1. `{"status":"start"}` — Tool begins execution (after LLM requests it)
2. `{"status":"ok"}` — Tool completed successfully, `result` carries output
3. `{"status":"err"}` — Tool failed, `error` carries the error message

#### `type:"usage"` — Token usage report

```json
{
  "type": "usage",
  "session_id": "<uuid>",
  "prompt_tokens": 1234,
  "completion_tokens": 56,
  "total_tokens": 1290,
  "estimated_context_tokens": 52000,
  "duration_ns": 1500000000,
  "cost": 0.00019,
  "total_cost": 0.00057,
  "ts": 1700000000123
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"usage"` |
| `session_id` | ✓ | Session UUID |
| `prompt_tokens` | ✓ | Input token count |
| `completion_tokens` | ✓ | Output token count |
| `total_tokens` | ✓ | Sum of prompt + completion |
| `estimated_context_tokens` | | Estimated context size before this LLM call (not present in history replay) |
| `duration_ns` | | Wall-clock time of LLM call in nanoseconds |
| `cost` | | Monetary cost of this LLM call in USD |
| `total_cost` | ✓ | Accumulated cost across the entire session |
| `ts` | | Unix timestamp in milliseconds |

#### `type:"quota"` — Provider balance info

```json
{
  "type": "quota",
  "provider": "ds-pro",
  "balance": 50.0,
  "currency": "USD",
  "ts": 1700000000123
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"quota"` |
| `provider` | ✓ | Model name (e.g. `"deepseek"`, `"claude"`) — matches the model config key |
| `balance` | | Money left / remaining balance |
| `currency` | | Currency code (e.g. `"CNY"`, `"USD"`) |
| `ts` | | Unix timestamp in milliseconds |

Emitted after each turn (best-effort) and on `get_session` / `model_switch`,
when the provider adapter is configured with a `quota` block in `hakka.json`.

**Configuration** (in `hakka.json`):

```json
{
  "ds-pro": {
    "quota": {
      "url": "https://api.deepseek.com/user/balance",
      "balance": ".balance_infos[currency=USD].total_balance",
      "currency": ".balance_infos[currency=USD].currency"
    }
  }
}
```

The `balance` and `currency` fields use simple dot+bracket path expressions
to extract values from the JSON response. Supported syntax:

| Syntax | Meaning |
|--------|---------|
| `.field` | Navigate into an object field |
| `.arr[0]` | Numeric array index |
| `.arr[f=v]` | Filter: find the first object where field `f` equals `v` |

If `currency` contains no dots or brackets, it is treated as a static literal
(e.g. `"USD"`).

#### `type:"req"` — Server request to client

```json
{"type":"req", "session_id":"<uuid>", "request_id":"req-1", "command":"vim.api.nvim_eval('1+1')", "ts": 1700000000123}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"req"` |
| `session_id` | ✓ | Session UUID |
| `request_id` | ✓ | Unique request ID (client must echo in `resp`) |
| `command` | ✓ | Client-specific command (e.g. Lua code for Neovim) |
| `ts` | | Unix timestamp in milliseconds |

The client **must** reply with a `type:"resp"` frame with the matching `request_id`.

#### `type:"result"` — Command execution result

```json
{"type":"result", "session_id":"<uuid>", "cmd":"session_info", "data":{"id":"...", "name":"...", ...}}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"result"` |
| `session_id` | ✓ | Session UUID |
| `cmd` | ✓ | The command that was executed |
| `data` | ✓ | Structured result data |
| `ts` | | Unix timestamp in milliseconds |

#### `type:"session"` — Session lifecycle event

```json
{
  "type": "session",
  "session_id": "<uuid>",
  "event": "get_session",
  "session": {"id": "...", ...},
  "events": [{"type": "chat", "text": "...", "ts": 1700000000123}, ...]
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `type` | ✓ | Always `"session"` |
| `session_id` | ✓ | Session UUID |
| `event` | ✓ | One of `"get_session"`, `"session_create"`, `"session_fork"`, `"renamed"`, `"session_delete"`, `"turn_started"`, `"turn_finished"` |
| `session` | | Session metadata map (present for `get_session`, `session_create`, `renamed`; absent for `session_delete`, `turn_started`, `turn_finished`) |
| `old_name` | | Previous session name (present on `"renamed"`) |
| `name` | | New session name (present on `"renamed"`) |
| `events` | | Array of typed events replaying session history (on `get_session`) |
| `ts` | | Unix timestamp in milliseconds |

The `events` field mirrors the live wire protocol — clients can render
history using the same code path as live streaming frames.

The `messages` field has been removed. Use `events` instead.

#### `type:"error"` — Protocol error

```json
{"type":"error", "session_id":"<uuid>", "error":"something went wrong"}
```

---

## Flow Diagrams

### Connection & Auto-Subscribe

```
Client                     Server
  │                          │
  │─── WebSocket Connect ───→│
  │                          │
  │←── welcome (sessions) ───│
  │←── session (auto-sub) ───│  (best session data + events)
  │                          │
  [Client can now send chat/cmd]
```

On connect:
1. Server sends `type:"welcome"` with all sessions in the namespace
2. Server auto-subscribes to the "best" session:
   - An in-flight session (one with an active turn), OR
   - The most recent session, OR
   - Nothing if no sessions exist
3. Auto-subscribe sends a `type:"session"` frame with session data and events replay

### Simple Chat Turn (Non-Streaming)

```
Client                     Server
  │                          │
  │─── chat (input:"Hi") ───→│
  │                          │── LLM Complete() ──→
  │                          │←──── response ──────
  │←── done (text:"Hello") ──│
```

1. Client sends `{"type":"chat", "input":"Hi"}`
2. Server calls LLM, gets complete response
3. Server sends `{"type":"done", "text":"Hello", "stats":{...}}`

### Streaming Chat Turn

```
Client                     Server
  │                          │
  │─── chat (input:"Hi") ───→│
  │                          │── LLM Stream() ──→
  │←── delta (text:"Hel") ───│
  │←── delta (text:"lo") ────│
  │←── delta (text:"!") ─────│
  │←── usage (tokens:...) ───│
  │←── done (text:"Hello!") ─│
```

1. Client sends `{"type":"chat", "input":"Hi"}`
2. Server streams LLM response chunk by chunk (adapter-controlled)
3. Each chunk arrives as `type:"delta"` with `text` field
4. After all deltas, server sends `type:"usage"` with token counts
5. Server sends `type:"done"` with the full reply and `stats`

### Tool Call Lifecycle

```
Client                     Server
  │                          │
  │─── chat (input:"add 2+3") ──→│
  │                          │── LLM Complete() ──→
  │                          │←── tool_calls ──────
  │←── tool (status:"start", id:"call_1", tool:"add") ──→│
  │                          │── execute add tool ──→
  │                          │←── result: 5 ────────
  │←── tool (status:"ok", id:"call_1", tool:"add", result:"5") ──→│
  │                          │── LLM Complete() ──→ (with tool result)
  │                          │←── "The answer is 5" ──
  │←── usage (tokens:...) ───│
  │←── done (text:"The answer is 5") ──│
```

### Tool Call Lifecycle During Streaming

```
Client                     Server
  │                          │
  │─── chat ────────────────→│
  │                          │── LLM Stream() ──→
  │←── delta (text:"Let me") │
  │←── delta (text:" check") │
  │                          │←── tool_calls ────  (stream detects tools)
  │←── tool (status:"start", id:"call_1") ───│
  │                          │── execute tool ───→
  │                          │←── result ────────
  │←── tool (status:"ok", id:"call_1") ───│
  │                          │── LLM Stream() ──→ (with tool result)
  │←── delta (text:"Here's")│
  │←── delta (text:" answer")│
  │←── usage (tokens:...) ───│
  │←── done ─────────────────│
```

### Command Flow

```
Client                     Server
  │                          │
  │─── cmd (command:{cmd:"session_info"}) ──→│
  │                          │
  │←── result (cmd:"session_info", data:{...}) ──│
```

### Get Session with Events Replay

```
Client                     Server
  │                          │
  │─── cmd (command:{cmd:"get_session", params:{id:"..."}}) ──→│
  │                          │
  │←── session (event:"get_session", session:{...}, events:[...]) ──│
```

The `events` array replays the session history as typed events:
- `{"type":"chat", "id":"...", "text":"...", "ts":...}` for user messages
- `{"type":"delta", "id":"...", "text":"...", "ts":...}` for assistant messages
- `{"type":"tool", "id":"...", "status":"start", ..., "ts":...}` for tool call starts
- `{"type":"usage", "total_tokens":..., "ts":...}` for usage reports
- `{"type":"tool", "id":"...", "status":"ok", ..., "ts":...}` for tool results
- `{"type":"done"}` terminal marker (only if session is NOT in-flight)

### Cancellation

```
Client                     Server
  │                          │
  │─── chat (input:"long task") ──→│
  │                          │── LLM Stream() ──→
  │←── delta (text:"...") ───│
  │─── cancel ───────────────→│
  │                          │── cancel context ──→
  │←── done (cancelled:true) │  (immediate acknowledgement)
  │                          │── turn winds down ──→
  │←── done (cancelled:true, stats:{...}) ──│  (turn termination)
```

The server sends an immediate `{"type":"done", "cancelled":true}` to the
client that sent the cancel. A second `done` frame with full `stats`
arrives when the turn actually terminates. If no turn is active, the
server responds with a `type:"error"` frame instead.

### Reconnection

All turn events (delta, tool, usage, done, req, renamed) and session
lifecycle events (session_create, renamed, session_delete, turn_started,
turn_finished) are broadcast to **every connected client in the namespace**
via the namespace hub. Clients filter by `session_id` to display only
events for the sessions they are viewing.

```
Client (disconnected)        Server
  │                          │── turn continues ──→
  │                          │   (broadcast to all connected clients)

Client (reconnected)         Server
  │─── WebSocket Connect ───→│
  │←── welcome ──────────────│
  │←── session (auto-sub) ───│
  │                          │── live events continue ──→ (already receiving)
```

A reconnecting client receives all current and future events immediately
after connecting — no explicit get_session or turn subscription is needed
for live events. The auto-subscribe frame on connect provides the initial
session history; live turn events arrive via the broadcast channel.

---

## Error Handling

### Protocol Errors

- Malformed JSON → `{"type":"error", "error":"..."}`
- Unknown frame type → `{"type":"error", "error":"unknown frame type: ..."}`
- Invalid frame structure → `{"type":"error", "error":"..."}`

### Turn Errors

Errors during LLM processing result in a `type:"done"` frame with `error` field:

```json
{"type":"done", "session_id":"...", "error":"LLM API error: ...", "stats":{...}}
```

### Tool Errors

Tool failures result in a `type:"tool"` frame with `status:"err"`:

```json
{"type":"tool", "id":"call_1", "tool":"read_file", "status":"err", "error":"file not found", "args":{...}}
```

---

## Key Rules for Client Authors

1. **Always include `session_id`** on every request once you have one.
2. **All events are broadcast to all clients** in the namespace. Every connected
   client receives ALL session lifecycle events and ALL turn events for ALL
   sessions. **Filter by `session_id`** to show only events relevant to the
   session(s) you are viewing.
3. **Streaming is adapter-controlled**: the server may emit `type:"delta"` frames
   during inference if the adapter supports streaming. Clients must always handle
   both delta and done frames regardless.
4. **`type:"done"` always arrives** — even on errors, even after cancel.
   It is your signal that the turn is complete.
5. **Tool events** (`type:"tool"`) and usage events (`type:"usage"`) may arrive
   interleaved with deltas. Each tool event carries a unique `id` field
   that correlates `status:"start"` with `status:"ok"`/`status:"err"`.
6. **Client requests** (`type:"req"`) are broadcast to ALL clients. The first
   client to reply with a matching `request_id` wins; the tool blocks until
   timeout if no client responds.
7. **Cancel** an in-flight turn by sending `{"type":"cancel", "session_id":"..."}`.
   The server replies with `{"type":"done", "cancelled":true}` immediately,
   followed by a second `done` with `stats` when the turn terminates.
   If no turn is active, the server replies with `{"type":"error"}`.
8. **Set CWD** before sending substantive input via `cwd_set` command
   (`{"type":"cmd", "command":{"cmd":"cwd_set", "params":{"cwd":"/path"}}}`).
9. **Enable tools** before asking the LLM to use them via `tool_allow` command
   (`{"type":"cmd", "command":{"cmd":"tool_allow", "params":{"name":"#all"}}}`).
10. **Rename sessions** with `session_rename` (manual) or `session_autorename`
   (LLM-generated name). Both return `type:"result"` with updated session metadata.
11. **Reconnect** — a reconnecting client receives all current and future
    events immediately via the broadcast channel. No explicit get_session
    or turn subscription is needed for live events.
