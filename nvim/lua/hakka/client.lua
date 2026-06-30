--- Hakka WebSocket client (v2 protocol).
---
--- Handles v2 frame types:
---   Inbound: welcome, delta, done, tool, usage, req, result, session, error
---   Outbound: chat, cmd, resp, cancel
local wsclient = require("hakka.wsclient")

local M = {}

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

--- Open a WebSocket connection, send a single request, and process responses.
--- Calls on_frame(parsed) for each incoming frame, and on_done(err) on completion.
--- Returns the connection object so callers can close it when needed.
local function connect_and_send(url, payload, on_frame, on_done)
  local connection

  --- Handle an incoming JSON frame from the server.
  --- This is called from a vim.schedule'd context (wsclient ensures this).
  local function handle_frame(text)
    local ok, parsed = pcall(vim.json.decode, text)
    if not ok then
      if connection then connection:close() end
      if on_done then on_done("json: " .. tostring(parsed)) end
      return
    end

    -- v2: "req" = server asks client to run code (e.g. Neovim Lua eval)
    if parsed.type == "req" and parsed.client_request then
      local req = parsed.client_request
      vim.schedule(function()
        local result, err = execute_vim_command(req.command)
        local resp = {
          type = "resp",
          request_id = req.request_id,
          result = result,
          error = err,
        }
        if connection then
          connection:send(resp)
        end
      end)
    -- v2: "done" = terminal frame
    elseif parsed.type == "done" then
      on_frame(parsed)
      if connection then
        connection:close()
      end
      if on_done then on_done(nil) end
    -- v2: "error" = terminal frame (before any turn)
    elseif parsed.type == "error" then
      on_frame(parsed)
      if connection then
        connection:close()
      end
      if on_done then on_done(nil) end
    else
      on_frame(parsed)
    end
  end

  connection = wsclient.connect(url, function(err)
    if err then
      if on_done then on_done(err) end
      return
    end

    -- Send the request
    connection:send(payload)

    -- Register frame callback (wsclient will deliver any buffered frames)
    connection:on_frame(handle_frame)

    -- Register close callback
    connection:on_close(function(close_err)
      if on_done then on_done(close_err) end
    end)
  end)

  return connection
end

--- Non-streaming: invoke on_response(err, parsed) once.
--- For non-streaming commands, the connection is closed after the first
--- non-"req" response frame is received (handles "session", "result",
--- "done", and "error" frame types).
function M.send(addr, payload, on_response)
  local got
  local conn = connect_and_send(addr, payload,
    function(frame)
      got = frame
      -- Close the connection after the first response frame (except "req"
      -- which requires a reply first). This ensures commands like get_session
      -- (which return a "session" frame, not "done") properly complete.
      if frame.type ~= "req" and conn then
        conn:close()
      end
    end,
    function(err)
      on_response(err, got)
    end)
end

--- Streaming: on_frame(parsed) is called for each frame until done/error.
function M.stream(addr, payload, on_frame, on_done)
  payload.stream = true
  connect_and_send(addr, payload, on_frame, on_done)
end

--- Execute a structured JSON command (non-streaming).
--- Sends {"type":"cmd","command":{"cmd":"...","params":{...}}}
function M.execute(addr, session_id, cmd, params, on_response)
  local payload = {
    type = "cmd",
    session_id = session_id or vim.NIL,
    command = { cmd = cmd, params = params or {} },
  }
  M.send(addr, payload, on_response)
end

return M
