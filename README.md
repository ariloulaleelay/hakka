# Hakka — LLM Agent Core Framework

[![Go Version](https://img.shields.io/github/go-mod/go-version/ariloulaleelay/hakka?logo=go&label=Go)](https://golang.org)
[![Build Status](https://img.shields.io/github/actions/workflow/status/ariloulaleelay/hakka/ci.yml?branch=main&logo=github)](https://github.com/ariloulaleelay/hakka/actions)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Hakka** is a minimal, modular, extensible LLM agent core framework written in Go. It provides an orchestration engine that drives the LLM ↔ tool iteration loop, with pluggable model providers, persistent sessions, and transport-agnostic gateways (TCP, WebSocket, Telegram). It ships with a first-class [Neovim plugin](nvim/hakka.nvim) that turns Neovim into an interactive agent IDE.

---

## Features

- **Pluggable Architecture** — Every component (LLM provider, persistence, tool, transport) is a Go interface. Swap or extend without touching core logic.
- **Concurrent Tool Execution** — Tools run in parallel when independent, speeding up complex workflows.
- **Transport Agnostic** — TCP, WebSocket, Telegram — the engine is fully isolated from how users connect.
- **Neovim Integration** — First-class plugin turns Neovim into an interactive agent IDE with status bars, session switching, and buffer inspection.
- **Persistent Sessions** — SQLite-backed (CGO-free via `modernc.org/sqlite`) or in-memory store with session history, token tracking, and metadata.
- **Cooperative Streaming** — Stream tokens by default; transparently falls back to tool loop when the model requests tools mid-stream.
- **Rich Tool System** — Built-in file, shell, search, HTTP, session management, and MCP server tools. Enable/disable per-session.
- **Runtime Model Switching** — Change the active model per-session with slash commands — no restart needed.
- **Zero CGO** — Pure Go SQLite, easy cross-compilation.

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

### Chat via TCP

```sh
echo '{"input":"Hello! What time is it?"}' | nc 127.0.0.1 9876

# Stream mode (token by token)
echo '{"input":"Count to 5","stream":true}' | nc 127.0.0.1 9876
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
| `--tcp-addr` | `127.0.0.1:9876` | TCP gateway bind address |
| `--ws-addr` | `:8765` | WebSocket gateway bind address |
| `--db` | *(in-memory)* | SQLite database file path |
| `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error` |

### Slash Commands

Slash commands are available in any input and are handled without invoking the LLM:

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/models` | List registered models (current marked with `*`) |
| `/model` | Show current active model |
| `/model <name>` | Switch session to a different model |
| `/session` | Show current session info |
| `/session list` | List all sessions |
| `/session create` | Create a new session |
| `/session switch <id>` | Switch to a different session |

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

## Wire Protocol

**Request** (TCP newline-JSON or WebSocket frame):

```json
{ "session_id": "<uuid>", "input": "...", "stream": true }
```

**Non-stream response** (single frame):

```json
{ "session_id": "...", "output": "...", "done": true }
```

**Stream response** (many delta frames, terminated by `done`):

```json
{ "session_id": "...", "delta": "chunk" }
...
{ "session_id": "...", "done": true }
```

If the model requests tools mid-stream, the gateway transparently falls back to a non-streaming tool loop and emits the final reply as a single `delta` followed by `done`.

**Observability events** (for Neovim and other rich clients):

```json
{ "session_id": "...", "event": "tool", "data": {"tool": "read_file", "status": "start"} }
```

---

## Architecture

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
                                             │  http_get            │  (HTML→Markdown)                       │
                                             │  search (ripgrep)    │
                                             │  vim_run_command     │
                                             └──────────────────────┘
```

---

## Neovim Integration

Hakka ships with a built-in Neovim plugin at [`nvim/hakka.nvim`](nvim/hakka.nvim).

### Installation

Using [lazy.nvim](https://github.com/folke/lazy.nvim):

```lua
{
  dir = "/path/to/hakka/nvim/hakka.nvim",
  config = function()
    require("hakka").setup({
      host = "127.0.0.1",
      port = 9876,
    })
  end,
}
```

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

# Run Neovim plugin tests
cd nvim && nvim --headless -c "lua dofile('tests/run.lua')" 2>&1
```

The project follows strong TDD conventions.


## Contributing

TBD
