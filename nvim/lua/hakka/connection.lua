--- Hakka Neovim plugin — persistent WebSocket connection.
---
--- Maintains a single WebSocket connection to the hakka server,
--- queues outbound messages when disconnected, and routes incoming
--- frames to registered handlers.
---
--- Frame routing:
---   - "welcome" → skipped (handled once on connect)
---   - "session" with event="get_session" → skipped only the first one
---     after welcome (auto-subscribe). Subsequent get_session frames
---     (from explicit commands) are processed.
---   - "req" → executes Lua in Neovim, sends response
---   - "delta", "tool", "usage" → routed to active turn handler
---   - "result", "session" (other events) → routed to command promise
---   - "done", "error" → terminal frames for the current request
---
--- API:
---   connection.setup(opts)       — initialize with config
---   connection:execute(sid, cmd, params, cb)  — non-streaming command
---   connection:chat(sid, text, handlers)      — streaming chat turn
---   connection:cancel(sid, cb)                — cancel active turn
---   connection:is_connected()    — check connection state
---   connection:get_ws()         — get ws object (for testing)
---   connection:close()          — close the connection

local wsclient = require("hakka.wsclient")

local M = {}

-- Configuration
local addr = "ws://127.0.0.1:8765/ws"

-- Connection state
local ws = nil
local connected = false
local auto_sub_skipped = false

-- Outbound message queue for when disconnected
local queue = {}

-- Active turn handlers (streaming)
-- Stores session_id so incoming frames from other sessions are not routed here.
local active_turn = nil -- { session_id, on_delta, on_tool, on_usage, on_done }

-- Command response handler queue
-- Each pending command registers { cmd, params, callback }
-- When a matching response frame arrives, the callback is called.
local pending_commands = {}

--- Execute a Lua command in Neovim and return the result.
--- Used to handle "req" frames from the server.
--- @param cmd string Lua code to execute
--- @return any, string|nil result, error
local function execute_vim_command(cmd)
  local fn, compile_err = load(cmd)
  if not fn then
    return nil, tostring(compile_err)
  end
  local ok, result = pcall(fn)
  if not ok then
    return nil, tostring(result)
  end
  return result, nil
end

--- Send a JSON frame over the WebSocket.
--- Queues the message if not connected.
--- @param payload table JSON-serializable payload
local function send_frame(payload)
  if not ws or not connected then
    table.insert(queue, payload)
    return
  end
  ws:send(payload)
end

--- Flush any queued messages after reconnection.
local function flush_queue()
  local queued = queue
  queue = {}
  for _, payload in ipairs(queued) do
    if ws then
      ws:send(payload)
    end
  end
end

--- Process an incoming JSON frame from the server.
--- @param text string Raw JSON text
local function handle_frame(text)
  local ok, parsed = pcall(vim.json.decode, text)
  if not ok then
    return
  end

  -- Type dispatch
  if parsed.type == "welcome" then
    -- Welcome on connect — skip it. It's not a response to any command.
    return
  end

  -- Auto-subscribe get_session: skip only the FIRST one after connect.
  if parsed.type == "session" and parsed.event == "get_session" and not auto_sub_skipped then
    auto_sub_skipped = true
    return
  end

  -- "req" frame: server asks us to execute Lua code.
  if parsed.type == "req" and parsed.request_id and parsed.command then
    vim.schedule(function()
      local result, err = execute_vim_command(parsed.command)
      send_frame({
        type = "resp",
        request_id = parsed.request_id,
        result = result,
        error = err,
      })
    end)
    return
  end

  -- Route to active turn handler (streaming frames).
  -- Frames are only routed if their session_id matches the active turn's session.
  -- Frames from other sessions (e.g. another client's streaming turn) are dropped.
  if active_turn then
    local session_matches = active_turn.session_id and
      active_turn.session_id == parsed.session_id

    if parsed.type == "delta" and parsed.text then
      if session_matches and active_turn.on_delta then
        active_turn.on_delta(parsed.text)
      end
      -- delta frames are always consumed (either delivered or dropped)
      return
    end

    if parsed.type == "tool" then
      if session_matches and active_turn.on_tool then
        local snippet = parsed.snippet or ""
        active_turn.on_tool(parsed.tool or "?", parsed.status, snippet,
          parsed.status == "ok" and { result = parsed.result } or nil)
      end
      return
    end

    if parsed.type == "usage" then
      if session_matches and active_turn.on_usage then
        active_turn.on_usage(parsed)
      end
      return
    end

    if parsed.type == "done" then
      if session_matches then
        local handlers = active_turn
        active_turn = nil -- clear before calling callback
        if handlers.on_done then
          local err
          if parsed.error and parsed.error ~= "" then
            err = parsed.error
          end
          handlers.on_done(err, parsed.stats)
        end
      end
      return
    end

    if parsed.type == "error" then
      if session_matches then
        local handlers = active_turn
        active_turn = nil
        if handlers.on_done then
          handlers.on_done(parsed.error or "unknown error", nil)
        end
      end
      return
    end
  end

  -- Route to pending command handler (non-streaming).
  if #pending_commands > 0 then
    -- Streaming frame types are never valid command responses — skip them.
    if parsed.type == "delta" or parsed.type == "tool" or parsed.type == "usage" then
      return
    end

    local entry = table.remove(pending_commands, 1)
    if entry.callback then
      entry.callback(nil, parsed)
    end
    return
  end
end

--- Open the WebSocket connection.
--- Called once by setup(). On reconnect, the queue is flushed.
local function connect()
  if ws then
    -- Already connecting or connected — skip.
    return
  end

  auto_sub_skipped = false

  ws = wsclient.connect(addr, function(err)
    if err then
      -- Connection failed. Clear the ws reference so we try again
      -- on the next operation that needs a connection.
      ws = nil
      connected = false
      return
    end
    connected = true
    auto_sub_skipped = false

    -- Register frame handler
    ws:on_frame(handle_frame)

    -- Register close handler
    ws:on_close(function(close_err)
      connected = false
      ws = nil

      -- Notify active turn that we disconnected
      if active_turn and active_turn.on_done then
        local handlers = active_turn
        active_turn = nil
        handlers.on_done("connection lost", nil)
      end

      -- Reject all pending commands
      while #pending_commands > 0 do
        local entry = table.remove(pending_commands, 1)
        if entry.callback then
          entry.callback("connection lost", nil)
        end
      end
    end)

    -- Flush any queued messages
    flush_queue()
  end)
end

--- Initialize the module and open the persistent connection.
--- @param opts table Configuration options
function M.setup(opts)
  if opts and opts.addr then
    addr = opts.addr
  end
  connect()
end

--- Check if the WebSocket is currently connected.
--- @return boolean
function M.is_connected()
  return connected
end

--- Get the raw wsclient connection object (for testing/mocking).
--- @return table|nil
function M.get_ws()
  return ws
end

--- Send a non-streaming JSON command and wait for a single response.
--- The connection is persistent; this uses a queue-based approach where
--- each execute() call registers a callback and the next incoming frame
--- (that doesn't match a streaming turn or auto-sub) is delivered to it.
---
--- @param session_id string|nil Session ID (may be nil for new sessions)
--- @param cmd string Command name (e.g. "session_create")
--- @param params table|nil Command parameters
--- @param callback fun(err: string|nil, resp: table|nil) Response callback
function M.execute(session_id, cmd, params, callback)
  if not connected then
    -- Try to (re)connect
    connect()
  end

  table.insert(pending_commands, {
    callback = callback,
  })

  send_frame({
    type = "cmd",
    session_id = session_id or vim.NIL,
    command = {
      cmd = cmd,
      params = params or {},
    },
  })
end

--- Start a streaming chat turn. Registers handlers for incoming delta,
--- tool, usage, and done frames. There can be at most one active turn
--- at a time; starting a new one replaces any existing handler.
---
--- @param session_id string The session to chat in
--- @param text string The user's message
--- @param handlers table { on_delta, on_tool, on_usage, on_done }
function M.chat(session_id, text, handlers)
  if not connected then
    connect()
  end

  -- Replace any existing active turn handler
  if active_turn and active_turn.on_done then
    active_turn.on_done("replaced by new turn", nil)
  end

  active_turn = {
    session_id = session_id,
    on_delta = handlers.on_delta,
    on_tool = handlers.on_tool,
    on_usage = handlers.on_usage,
    on_done = handlers.on_done,
  }

  send_frame({
    type = "chat",
    session_id = session_id,
    input = text,
    stream = true,
  })
end

--- Cancel an in-flight chat turn.
--- @param session_id string The session to cancel
--- @param callback fun(err: string|nil, resp: table|nil)
function M.cancel(session_id, callback)
  if active_turn then
    -- Clear active turn — server will respond with done frame
    -- But we still need to forward the done response to the callback
    local old_on_done = active_turn.on_done
    active_turn.on_done = function(err, stats)
      if old_on_done then
        old_on_done(err, stats)
      end
      if callback then
        callback(nil, { cancelled = true, stats = stats })
      end
    end
  end

  send_frame({
    type = "cancel",
    session_id = session_id,
  })

  -- If no active turn, just wait for the done response
  if not active_turn then
    table.insert(pending_commands, {
      callback = function(err, resp)
        if callback then
          callback(err, resp)
        end
      end,
    })
  end
end

--- Close the WebSocket connection explicitly.
function M.close()
  if ws then
    ws:close()
  end
  ws = nil
  connected = false
  active_turn = nil
  pending_commands = {}
  queue = {}
end

return M
