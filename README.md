# hakka

A minimal, extensible LLM agent core framework in Go.

## Concepts

- **Session** — append-only conversation, optional state bag.
- **SessionManager / SessionStore** — pluggable persistence (`MemoryStore`, `sqlite.Store`).
- **LLMAdapter** — `Complete` and `Stream` against any provider.
- **ToolRegistry** — register async `Tool`s with JSON-schema parameters and optional `Timeout`.
- **Engine** — drives the LLM ↔ tool loop until the model returns a final
  message or `MaxToolIterations` is exhausted. Concurrent tool execution,
  lifecycle `Hooks`, structured `slog.Logger`.
- **Gateway** — transports that feed user input into the engine (TCP, WebSocket, Telegram).

## Run the example

```sh
# Set required environment variables first
export CUSTOM_ENDPOINT="https://api.example.com/internal"
export CUSTOM_TOKEN="your-token-here"

make build
./bin/hakka --config cmd/hakka/hakka.json --db ~/.hakka.db
```

Useful flags:

| Flag           | Default                             | Notes                              |
| -------------- | ----------------------------------- | ---------------------------------- |
| `--config`     | `cmd/hakka/hakka.json`            | Model registry JSON                |
| `--tcp-addr`   | `127.0.0.1:9876`                    | TCP gateway bind                   |
| `--ws-addr`    | `:8765`                             | WebSocket gateway bind             |
| `--db`         | _empty_ (in-memory)                 | SQLite DB path                     |
| `--log-level`  | `info`                              | `debug`, `info`, `warn`, `error`   |

### Model config (`hakka.json`)

```json
{
  "default": "deepseek",
  "models": {
    "deepseek": {
      "dialect": "openai",
      "base_url": "${env: CUSTOM_ENDPOINT}/deepseek-v3-1-terminus/v1",
      "model": "deepseek-latest",
      "headers": {
        "Authorization": "OAuth ${env: CUSTOM_TOKEN}",
        "Need-Raw-Answer": "true"
      }
    },
    "claude": {
      "dialect": "anthropic",
      "base_url": "${env: CUSTOM_ENDPOINT}/anthropic/v1",
      "model": "claude-opus-4-7",
      "headers": {"Authorization": "OAuth ${env: CUSTOM_TOKEN}", "Need-Raw-Answer": "true"},
      "extra": {"anthropic_version": "2023-06-01", "max_tokens": 2048}
    },
    "gemini": {
      "dialect": "gemini",
      "base_url": "${env: CUSTOM_ENDPOINT}/google/v1beta",
      "model": "gemini-3.1-pro-preview",
      "headers": {"Authorization": "OAuth ${env: CUSTOM_TOKEN}", "Need-Raw-Answer": "true"}
    }
  }
}
```

The `${env: VAR_NAME}` placeholder is substituted from the process environment.
If a referenced environment variable is unset or empty, hakka exits with an error.
Keys under `models` are free-form; you can register the same provider multiple times
with different model ids (e.g. `"fast": claude-haiku`, `"smart": claude-opus`).

### Switching models at runtime

Use slash commands in any user input:

```
/models           # list, current is marked with *
/model            # show current
/model gemini     # switch this session to the "gemini" entry
```

```sh
# TCP (newline-delimited JSON)
echo '{"input":"what time is it?"}' | nc 127.0.0.1 9876

# Stream mode
echo '{"input":"count to 5","stream":true}' | nc 127.0.0.1 9876

# WebSocket
websocat ws://127.0.0.1:8765/ws
> {"input":"add 2 and 3"}
```

## Wire protocol

**Request** (TCP newline-JSON or WS frame):

```json
{ "session_id": "<uuid>", "input": "...", "stream": true }
```

**Non-stream response**: single frame

```json
{ "session_id": "...", "output": "...", "done": true }
```

**Stream response**: many delta frames terminated by `done`

```json
{ "session_id": "...", "delta": "chunk" }
...
{ "session_id": "...", "done": true }
```

If the model requests tools mid-stream, the gateway transparently falls back
to a non-streaming tool loop (`Engine.Resume`) and emits the final reply as
a single `delta` followed by `done`.

## Extending

| Need                  | Implement                                   |
| --------------------- | ------------------------------------------- |
| New LLM provider      | `agent.LLMAdapter`                          |
| Persistent sessions   | `agent.SessionStore` (e.g. `stores/sqlite`) |
| New tool              | `agent.Tool` + `ToolRegistry.Register`      |
| New transport         | `gateways.Gateway`                          |
| Middleware/guardrails | Engine `Hooks` or wrap `Engine.Chat`        |
