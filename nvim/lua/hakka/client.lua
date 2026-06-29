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

local function safe_encode(val)
  if val == nil then
    return "null"
  end
  local ok, encoded = pcall(vim.json.encode, val)
  if ok then
    return encoded
  end
  return '"' .. tostring(val):gsub('["\\]', function(c) return '\\' .. c end) .. '"'
end

--- Open a WebSocket connection, send a single request, and process responses.
--- Calls on_frame(parsed) for each incoming frame, and on_done(err) on completion.
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

    if parsed.event == "client_request" and parsed.client_request then
      local req = parsed.client_request
      vim.schedule(function()
        local result, err = execute_vim_command(req.command)
        local resp = {
          type = "response",
          request_id = req.request_id,
          result = result,
          error = err,
        }
        if connection then
          connection:send(resp)
        end
      end)
    else
      -- We are already in a vim.schedule'd context, so call on_frame directly
      on_frame(parsed)
      if parsed.done or (parsed.error and parsed.error ~= "") then
        if connection then
          connection:close()
        end
        if on_done then on_done(nil) end
      end
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
end

-- Non-streaming: invoke on_response(err, parsed) once.
function M.send(addr, payload, on_response)
  local got
  connect_and_send(addr, payload,
    function(frame) got = frame end,
    function(err)
      on_response(err, got)
    end)
end

-- Streaming: on_frame(parsed) is called for each frame until done/error.
function M.stream(addr, payload, on_frame, on_done)
  payload.stream = true
  connect_and_send(addr, payload, on_frame, on_done)
end

-- Execute a structured JSON command (non-streaming).
-- Builds the proper command frame and returns the response.
function M.execute(addr, session_id, cmd, params, on_response)
  local payload = {
    session_id = session_id or vim.NIL,
    command = { cmd = cmd, params = params or {} },
  }
  M.send(addr, payload, on_response)
end

return M
