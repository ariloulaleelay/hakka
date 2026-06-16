local config = require("hakka.config")
local client = require("hakka.client")
local ui = require("hakka.ui")

local M = {}

local session_id = nil
local pending = false

function M.setup(opts)
  config.setup(opts)
end

--- Send init handshake to the server. Creates/resolves a session,
--- stores the current working directory, and returns metadata.
--- @param on_done fun(err: string|nil, resp: table|nil)
local function send_init(on_done)
  local addr = config.values.addr
  local payload = {
    type = "init",
    cwd = vim.fn.getcwd(),
    start = true,
  }
  client.send(addr, payload, function(err, resp)
    if err then
      ui.append_error("init: " .. err)
      if on_done then on_done(err, nil) end
      return
    end
    if resp.error and resp.error ~= "" then
      ui.append_error("init: " .. resp.error)
      if on_done then on_done(resp.error, nil) end
      return
    end
    -- Store session ID from server response
    if resp.session_id and resp.session_id ~= "" then
      session_id = resp.session_id
      ui.set_session_id(session_id)
    end
    -- Show model info in status bar
    if resp.data and resp.data.model then
      ui.set_model(resp.data.model)
    end
    if on_done then on_done(nil, resp) end
  end)
end

local function send(text)
  if pending then
    ui.append_error("previous request still in flight")
    return
  end
  pending = true

  -- Client-side intercept to clear active session if deleted
  local trimmed = vim.trim(text)
  if trimmed:match("^/session%s+delete%s*(.*)$") then
    local target = trimmed:match("^/session%s+delete%s*(.*)$")
    if target == session_id or target == "" or target == "this" then
      session_id = nil
      ui.set_session_id(nil)
    end
  end

  ui.append_user(text)
  ui.begin_assistant_stream()
  local saw_error = false
  local payload = {
    session_id = session_id,
    input = text,
  }
  -- CWD is NOT sent here — it was established during init handshake.
  client.stream(config.values.addr, payload, function(frame)
    if frame.event == "session_switch" or frame.event == "session_create" then
      ui.clear()
      if frame.data and frame.data.messages then
        ui.load_history(frame.data.messages)
      end
    end
    if frame.session_id and frame.session_id ~= "" then
      session_id = frame.session_id
      ui.set_session_id(session_id)
    end
    if frame.error and frame.error ~= "" then
      saw_error = true
      ui.append_error(frame.error)
      return
    end
    if frame.event == "tool" then
      if frame.status == "start" or frame.status == "ok" or frame.status == "err" then
        ui.append_tool_event(frame.tool or "?", frame.status, frame.exec_snippet)
      end
      return
    end
    if frame.event == "meta" and frame.data then
      ui.append_meta_event(frame.data)
      return
    end
    if frame.delta and frame.delta ~= "" then
      ui.append_delta(frame.delta)
    end
  end, function(err)
    pending = false
    if err and not saw_error then
      ui.append_error(err)
      return
    end
    ui.end_assistant_stream()
  end)
end

function M.toggle()
  if ui.is_open() then
    ui.close()
  else
    ui.open(send, M.cancel)
    -- Send init handshake when the UI opens: creates a fresh session
    -- with all tools enabled (start=true), stores current working
    -- directory, fetches model info. All in one round-trip.
    send_init()
  end
end

function M.send_oneshot(text)
  local payload = {
    session_id = session_id,
    input = text,
  }
  client.send(config.values.addr, payload, function(err, resp)
    if err then
      vim.notify("hakka: " .. err, vim.log.levels.ERROR)
      return
    end
    if resp.error and resp.error ~= "" then
      vim.notify("hakka: " .. resp.error, vim.log.levels.ERROR)
      return
    end
    if resp.session_id then
      session_id = resp.session_id
      ui.set_session_id(session_id)
    end
    vim.notify(resp.output or "", vim.log.levels.INFO)
  end)
end

function M.reset()
  session_id = nil
  ui.set_session_id(nil)
  ui.set_model(nil)
  vim.notify("hakka: session reset", vim.log.levels.INFO)
end

--- Cancel the currently in-flight request.
function M.cancel()
  if not pending then
    return
  end
  if not session_id then
    return
  end
  local payload = {
    type = "cancel",
    session_id = session_id,
  }
  client.send(config.values.addr, payload, function(err, resp)
    if err then
      vim.notify("hakka: cancel error: " .. err, vim.log.levels.ERROR)
      return
    end
  end)
end

return M
