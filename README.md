---
# Hakka — LLM Agent Core Framework

[![Go Version](https://img.shields.io/github/go-mod/go-version/ariloulaleelay/hakka?logo=go&label=Go)](https://golang.org)
[![Build Status](https://img.shields.io/github/actions/workflow/status/ariloulaleelay/hakka/ci.yml?branch=main&logo=github)](https://github.com/ariloulaleelay/hakka/actions)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Hakka** is a minimal, modular, extensible LLM agent core framework written in Go. It provides an orchestration engine that drives the LLM ↔ tool iteration loop, with pluggable model providers, persistent sessions, and transport-agnostic gateways (WebSocket, Telegram). It pairs with a dedicated [hakka.nvim](https://github.com/ariloulaleelay/hakka.nvim) plugin that turns Neovim into an interactive agent IDE.

---

## Features

- **Pluggable Architecture** — Every component (LLM provider, persistence, tool, transport) is a Go interface. Swap or extend without touching core logic.
- **Concurrent Tool Execution** — Tools run in parallel when independent, speeding up complex workflows.
- **Transport Agnostic** — WebSocket, Telegram — the engine is fully isolated from how users connect.
- **Neovim Integration** — Dedicated [hakka.nvim](https://github.com/ariloulaleelay/hakka.nvim) plugin turns Neovim into an interactive agent IDE with status bars, session fetching, and buffer inspection.
- **Persistent Sessions** — SQLite-backed (CGO-free via `modernc.org/sqlite`) or in-memory store with session history, token tracking, LLM latency measurement, and metadata.
- **Cooperative Streaming** — Stream tokens by default; transparently falls back to tool loop when the model requests tools mid-stream.
- **Rich Tool System** — Built-in file, shell, search, HTTP, session management, and MCP server tools. Enable/disable per-session.
- **Runtime Model Switching** — Change the active model per-session with slash commands — no restart needed.
- **Zero CGO** — Pure Go SQLite, easy cross-compilation.
- **Batch Mode** — Run autonomous tasks without starting servers, ideal for scripting and CI/CD integration.

---

## Quick Start

### Prerequisites

- Go 1.25+ ([download](https://go.dev/dl/))
- A supported LLM API key (DeepSeek, OpenAI, Anthropic, or Google Gemini)

### Install & Build

```sh
# Install from source
git clone https://github.com/ariloulaleelay/hakka.git
cd hakka
make build

# Or install directly
go install github.com/ariloulaleelay/hakka/cmd/hakka@latest
```

### Minimal Configuration

Create a `hakka.json`:

```json
{
  "default": "deepseek",
  "models": {
    "deepseek": {
      "dialect": "openai",
      "base_url": "https://api.deepseek.com",
      "model": "deepseek-chat",
      "headers": {
        "Authorization": "Bearer ${env: DEEPSEEK_API_KEY}"
      }
    }
  }
}
```

### Run

```sh
export DEEPSEEK_API_KEY="sk-your-key-here"
./bin/hakka --config hakka.json --db ~/.hakka.db

# Or with the example config
export DEEPSEEK_API_KEY="sk-your-key-here"
./bin/hakka --config hakka.example.json --db ~/.hakka.db
```

### Chat via WebSocket

```sh
websocat ws://127.0.0.1:8765/ws
> {"input":"Add 2 and 3"}
```

---

## Usage

### Command-Line Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `hakka.json` | Model registry configuration file |
| `--ws-addr` | `:8765` | WebSocket gateway bind address |
| `--db` | *(in-memory)* | SQLite database file path |
| `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `--run` | — | **Batch mode**: run a single autonomous task (no servers started) |
| `--run-file` | — | **Batch mode**: read task from file and run it |
| `--run-enable-tool` | — | **Batch mode**: comma-separated tool names or tags to enable. Use `#tag` to force tag resolution (e.g. `--run-enable-tool "#utility,read_file"`) |

### Batch Mode

Run a single autonomous task without starting any network gateways. The agent
executes the task using the configured LLM, prints the final response to stdout,
writes session info (ID + token usage) to stderr, and exits.

```sh
# Run a simple task
./bin/hakka --config hakka.json --run "What files are in the current directory?"

# Read task from a file
./bin/hakka --config hakka.json --run-file task.txt

# Limit which tools the agent can use (whitelist by name or tag)
./bin/hakka --config hakka.json \
  --run "Summarize TODO.md" \
  --run-enable-tool "read_file,search"

# Enable only safe utility tools using the #tag syntax
./bin/hakka --config hakka.json \
  --run "Generate a random number" \
  --run-enable-tool "#utility"

# Enable a specific tool by name alongside a group by tag
./bin/hakka --config hakka.json \
  --run "Create a file called hello.txt" \
  --run-enable-tool "write_file,#utility"

# Use # prefix when a tag name collides with a tool name
./bin/hakka --config hakka.json \
  --run "List all tools" \
  --run-enable-tool "#all"  # forces tag resolution, avoids tool name "all"
```

Batch mode uses an **in-memory session store** — no database is created or
persisted. MCP servers from the configuration are connected and available
during the run.

Tool tags available for `--run-enable-tool`:

| Tag | Included tools |
|-----|---------------|
| `filesystem` | `read_file`, `list_dir`, `write_file`, `edit_file`, `search` |
| `network` | `http_get` |
| `exec` | `shell` |
| `utility` | `random`, `echo`, `feedback`, session tools |
| `vim` | `vim_list_buffers`, `vim_read_buffer` |
| `session` | All session management tools |
| `all` | Every tool |

### Slash Commands

Slash commands are converted to JSON commands **client-side**. Clients (like the Neovim plugin or the Telegram gateway) parse text like `/help` and send the corresponding JSON `command` object. This is a **non-exhaustive** list of available slash commands and their JSON equivalents:

| Slash command | JSON command |
|---|---|
| `/help` | `{"cmd":"help"}` |
| `/models` | `{"cmd":"model_list"}` |
| `/model` | `{"cmd":"session_info"}` |
| `/model <name>` | `{"cmd":"model_switch","params":{"name":"<name>"}}` |
| `/session` | `{"cmd":"session_info"}` |
| `/session list` | `{"cmd":"session_list"}` |
| `/session create` | `{"cmd":"session_create"}` |
| `/session get <id>` | `{"cmd":"get_session","params":{"id":"<id>"}}` |
| `/session info` | `{"cmd":"session_info"}` |
| `/session rename <name>` | `{"cmd":"session_rename","params":{"name":"<name>"}}` |
| `/continue` | `{"cmd":"continue"}` |
| `/cwd_set <path>` | `{"cmd":"cwd_set","params":{"cwd":"<path>"}}` |
| `/compact [n]` | `{"cmd":"compact","params":{"n":<n>}}` or `{}` to read |
| `/tool list` | `{"cmd":"tool_list"}` |
| `/tool allow <name>` | `{"cmd":"tool_allow","params":{"name":"<name>"}}` |
| `/tool deny <name>` | `{"cmd":"tool_deny","params":{"name":"<name>"}}` |
| `/start` | `{"cmd":"start"}` |

See AGENT.md for the full list of JSON commands and protocol details.

### Model Switching at Runtime

```sh
/model gemini     # switch this session to the "gemini" model entry
/models           # list all, current is marked with *
```

---

## Configuration

### Model Registry (`hakka.json`)

```json
{
  "default": "deepseek",
  "models": {
    "deepseek": {
      "dialect": "openai",
      "base_url": "https://api.deepseek.com",
      "model": "deepseek-chat",
      "headers": {
        "Authorization": "Bearer ${env: DEEPSEEK_API_KEY}"
      }
    },
    "claude": {
      "dialect": "anthropic",
      "base_url": "https://api.anthropic.com/v1",
      "model": "claude-sonnet-4-20250514",
      "headers": {
        "x-api-key": "${env: ANTHROPIC_API_KEY}",
        "anthropic-version": "2023-06-01"
      },
      "extra": {
        "max_tokens": 2048
      }
    },
    "gemini": {
      "dialect": "gemini",
      "base_url": "https://generativelanguage.googleapis.com/v1beta",
      "model": "gemini-2.5-flash",
      "headers": {
        "x-goog-api-key": "${env: GEMINI_API_KEY}"
      }
    }
  }
}
```

- `${env: VAR_NAME}` placeholders are substituted from the process environment at startup.
- If a referenced environment variable is unset or empty, hakka exits with an error.
- Keys under `models` are free-form; you can register the same provider multiple times with different model IDs (e.g. `"fast"` for Haiku, `"smart"` for Opus).

### Pricing Configuration

For providers that don't return cost in their API response (like DeepSeek), you can
configure per-token pricing and Hakka will calculate the monetary cost automatically:

```json
{
  "default": "deepseek",
  "models": {
    "deepseek": {
      "dialect": "openai",
      "base_url": "https://api.deepseek.com",
      "model": "deepseek-chat",
      "headers": {
        "Authorization": "Bearer ${env: DEEPSEEK_API_KEY}"
      },
      "pricing": {
        "input": 0.00000027,
        "output": 0.00000110,
        "cache_hit_input": 0.00000007
      }
    }
  }
}
```

Pricing fields (per-token USD):
- **`input`** — cost per prompt token
- **`output`** — cost per completion token
- **`cache_hit_input`** — (optional) reduced cost per cached prompt token, used when the
  provider reports `prompt_cache_hit_tokens` and `prompt_cache_miss_tokens` separately
  (e.g. DeepSeek)

When a provider returns cost in its response (e.g. OpenRouter), the response cost takes
precedence over the configured pricing.

### MCP Server Integration

```json
{
  "mcp_servers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    },
    "calendar": {
      "command": "python3",
      "args": ["/path/to/calendar_server.py"]
    }
  }
}
```

MCP tools are automatically discovered on startup and registered in the tool registry.

---

## Wire Protocol (v2)

**Request** (WebSocket frame):

```json
{"type":"chat", "session_id":"<uuid>", "input":"...", "stream":true}
```

**Non-stream response** (single `type:"done"` frame):

```json
{"type":"done", "session_id":"...", "output":"...", "stats":{"total_tokens":..., "model":"..."}}
```

**Stream response** (many `type:"delta"` frames, terminated by `type:"done"`):

```json
{"type":"delta", "session_id":"...", "text":"chunk"}
...
{"type":"done", "session_id":"...", "stats":{...}}
```

**Observability events** (tool lifecycle, token usage):

```json
{"type":"tool", "session_id":"...", "id":"call_1", "tool":"read_file", "status":"start", "args":{"path":"README.md"}}
{"type":"usage", "session_id":"...", "prompt_tokens":1234, "completion_tokens":56, "total_tokens":1290}
```

**Welcome on connect** (no more `list_sessions` dance):

```json
{"type":"welcome", "protocol_version":"2", "data":{"sessions":[...]}}
```

See `protocol.md` or `AGENT.md` for the full protocol specification.

---

## Architecture

```
                         ┌─────────────────────────────────────┐
                         │           GATEWAYS                  │
                         │  WebSocket · Telegram               │
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

---

## Neovim Integration

Hakka connects to Neovim through the dedicated [hakka.nvim](https://github.com/ariloulaleelay/hakka.nvim) plugin.
It connects to the hakka WebSocket gateway using a pure-Lua WebSocket client
built on libuv (`ws://127.0.0.1:8765/ws` by default).

### Features

- Interactive chat buffer with agent responses rendered in real-time
- Session management (create, switch, list)
- Model switching via commands
- Status line integration (`vim.g.hakka_status`)
- Buffer inspection tools (`vim_list_buffers`, `vim_read_buffer`)

---

## Extending

| Need | Interface/Component |
|------|-------------------|
| New LLM provider | `agent.LLMAdapter` |
| Persistent session store | `agent.SessionStore` (e.g. `stores/sqlite`) |
| New tool | `agent.Tool` + `ToolRegistry.Register` |
| New transport | Implement a Gateway |
| Middleware/guardrails | Engine `Hooks` or wrap `Engine.Chat` |

---

## Testing

```sh
# Run all Go tests
make test

# With coverage
make cover
```

The project follows strong TDD conventions.


## Contributing

TBD
