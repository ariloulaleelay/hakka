local config = require("hakka.config")
local client = require("hakka.client")
local ui = require("hakka.ui")

local M = {}

local session_id = nil
local session_name = nil
local pending = false
local _cancelling = false

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
    if resp.session_id and resp.session_id ~= "" then
      session_id = resp.session_id
      ui.set_session_id(session_id)
    end
    if resp.data and resp.data.model then
      ui.set_model(resp.data.model)
    end
    -- Fetch session list on connect (silently)
    client.execute(addr, session_id, "session_list", {}, function(exec_err, exec_resp)
      if exec_err or not exec_resp then
        return
      end
      -- Silently update state without showing in chat
      if exec_resp.event == "command_result" and exec_resp.data and exec_resp.data.sessions then
        -- Store in global for status bar / later use
        -- No UI output — this is a background refresh
      end
    end)
    if on_done then on_done(nil, resp) end
  end)
end

--- Map a slash command text to a JSON command.
--- Returns {cmd, params} or nil if not a known command.
local function parse_slash_command(text)
  local trimmed = vim.trim(text)
  if not trimmed:match("^/") then
    return nil
  end
  local parts = {}
  for part in trimmed:gmatch("%S+") do
    table.insert(parts, part)
  end
  local cmd = parts[1]
  if not cmd then return nil end

  -- Strip @bot_username suffix
  cmd = cmd:gsub("@.*$", "")

  -- /help
  if cmd == "/help" then
    return { cmd = "help", params = {} }
  end

  -- /continue — NOT intercepted as JSON command, must go through
  -- the LLM path (sends as regular input so server invokes the engine)
  if cmd == "/continue" then
    return nil
  end

  -- /start
  if cmd == "/start" then
    return { cmd = "start", params = {} }
  end

  -- /compact [n]
  if cmd == "/compact" then
    local n = tonumber(parts[2])
    return { cmd = "compact", params = n and { n = n } or {} }
  end

  -- /session list | create | info | autorename
  if cmd == "/session" then
    local sub = parts[2]
    if sub == "list" then
      return { cmd = "session_list", params = {} }
    elseif sub == "create" then
      return { cmd = "session_create", params = {} }
    elseif sub == "info" then
      return { cmd = "session_info", params = {} }
    elseif sub == "autorename" then
      return { cmd = "session_autorename", params = {} }
    elseif sub == "delete" then
      local target = parts[3] or ""
      return { cmd = "session_delete", params = { id = target } }
    elseif sub == "switch" then
      local target = parts[3] or ""
      return { cmd = "session_switch", params = { id = target } }
    elseif sub == "rename" then
      -- Join remaining parts as the name
      local name_parts = {}
      for i = 3, #parts do
        table.insert(name_parts, parts[i])
      end
      local name = table.concat(name_parts, " ")
      return { cmd = "session_rename", params = { name = name } }
    end
    return nil -- unknown session subcommand, let server handle via text
  end

  -- /model list | show | switch <name>
  if cmd == "/models" then
    return { cmd = "model_list", params = {} }
  end
  if cmd == "/model" then
    local sub = parts[2]
    if sub == "list" then
      return { cmd = "model_list", params = {} }
    elseif sub == "show" then
      return { cmd = "session_info", params = {} } -- shows session info with model
    elseif sub == "switch" then
      local name = parts[3]
      if name then
        return { cmd = "model_switch", params = { name = name } }
      end
    end
    -- /model alone or with unknown sub
    if sub and sub ~= "list" and sub ~= "show" and sub ~= "switch" then
      -- treat as /model <name> (short form)
      return { cmd = "model_switch", params = { name = sub } }
    end
    return { cmd = "session_info", params = {} }
  end

  -- /tool list | enable | disable
  if cmd == "/tool" then
    local sub = parts[2]
    if sub == "list" then
      return { cmd = "tool_list", params = {} }
    elseif sub == "enable" then
      local name = parts[3]
      if name then
        return { cmd = "tool_enable", params = { name = name } }
      end
    elseif sub == "disable" then
      local name = parts[3]
      if name then
        return { cmd = "tool_disable", params = { name = name } }
      end
    end
    return nil
  end

  return nil
end

-- Handle a command_result frame (from a JSON command).
local function handle_command_result(frame)
  local cmd = frame.cmd
  local data = frame.data or {}

  if cmd == "session_create" or cmd == "start" then
    if data.session then
      session_id = data.session.id
      session_name = data.session.name
      ui.set_session_id(session_id)
      ui.clear()
    end
  elseif cmd == "session_switch" then
    if data.session then
      session_id = data.session.id
      session_name = data.session.name
      ui.set_session_id(session_id)
    end
    -- Populate the UI with session history
    if data.messages then
      -- If we already have a UI, load history into it
      if ui.is_open() then
        ui.clear()
        ui.load_history(data.messages)
      end
    else
      ui.clear()
    end
  elseif cmd == "session_delete" then
    if data.active_cleared then
      session_id = nil
      session_name = nil
      ui.set_session_id(nil)
      ui.clear()
    end
  elseif cmd == "session_info" then
    if data.session then
      local s = data.session
      local lines = {
        "Session Info:",
        string.format("  ID:          %s", s.id or "?"),
        string.format("  Name:        %s", s.name or "(unnamed)"),
        string.format("  Model:       %s", s.model or "?"),
        string.format("  Messages:    %d", s.message_count or 0),
        string.format("  Total Tokens: %d", s.total_tokens or 0),
        string.format("  Context:     ~%d tokens", s.estimated_context or 0),
      }
      if s.compact_soft_limit then
        table.insert(lines, string.format("  Compact:     %d tokens", s.compact_soft_limit))
      end
      ui.append_assistant(table.concat(lines, "\n"))
    end
  elseif cmd == "session_rename" then
    if data.session then
      session_name = data.session.name
      ui.set_session_id(data.session.id)
      ui.append_assistant("Session renamed to: " .. (data.session.name or ""))
    end
  elseif cmd == "session_autorename" then
    if data.session and data.session.name then
      session_name = data.session.name
      ui.set_session_id(data.session.id)
      ui.append_assistant("Session renamed to: " .. data.session.name)
    end
  elseif cmd == "session_delete" then
    if data.deleted then
      if data.active_cleared then
        ui.append_assistant("Session " .. data.deleted .. " deleted (active session cleared)")
      else
        ui.append_assistant("Session " .. data.deleted .. " deleted")
      end
    end
  elseif cmd == "session_list" then
    if data.sessions and #data.sessions > 0 then
      local lines = { "Sessions:" }
      for _, s in ipairs(data.sessions) do
        local mark = s.current and "* " or "  "
        local name = (s.name and s.name ~= "") and s.name or s.short_id or s.id
        local count = s.message_count or 0
        table.insert(lines, string.format("%s%-20s [%d msgs] %s", mark, name, count, s.id))
      end
      ui.append_assistant(table.concat(lines, "\n"))
    else
      ui.append_assistant("No sessions found.")
    end
  elseif cmd == "model_switch" then
    if data.model then
      ui.set_model(data.model)
    end
  elseif cmd == "model_list" then
    if data.models then
      local lines = { "Models:" }
      for _, m in ipairs(data.models) do
        local mark = m.current and "* " or "  "
        table.insert(lines, mark .. m.name)
      end
      ui.append_assistant(table.concat(lines, "\n"))
    end
  elseif cmd == "tool_list" then
    if data.tools then
      local lines = { "Tools:" }
      for _, t in ipairs(data.tools) do
        local status = t.enabled and "[enabled]" or "[disabled]"
        local tags = ""
        if t.tags and #t.tags > 0 then
          tags = "  (#" .. table.concat(t.tags, ", #") .. ")"
        end
        table.insert(lines, string.format("  %-25s %s%s", t.name, status, tags))
      end
      ui.append_assistant(table.concat(lines, "\n"))
    end
  elseif cmd == "tool_enable" then
    if data.enabled then
      ui.append_assistant("enabled: " .. table.concat(data.enabled, ", "))
    end
  elseif cmd == "tool_disable" then
    if data.disabled then
      ui.append_assistant("disabled: " .. table.concat(data.disabled, ", "))
    end
  elseif cmd == "help" then
    if data.commands then
      local lines = { "Available commands:" }
      for _, c in ipairs(data.commands) do
        local extra = ""
        if c.params then
          local param_str = {}
          for k, v in pairs(c.params) do
            table.insert(param_str, k .. "=" .. v)
          end
          extra = "  " .. table.concat(param_str, ", ")
        end
        table.insert(lines, string.format("  %-20s %s%s", c.cmd, c.desc, extra))
      end
      ui.append_assistant(table.concat(lines, "\n"))
    end
  elseif cmd == "compact" then
    if data.compact_soft_limit then
      ui.append_assistant("compact soft limit: " .. data.compact_soft_limit)
    end
  end
end

local function send(text)
  if pending then
    ui.append_error("previous request still in flight")
    return
  end

  -- Try to intercept as a JSON command
  local json_cmd = parse_slash_command(text)
  if json_cmd then
    ui.append_user(text)
    ui.begin_assistant_stream()

    local function on_command_response(err, resp)
      pending = false
      if err then
        ui.append_error(err)
        return
      end
      if resp.error and resp.error ~= "" then
        ui.append_error(resp.error)
        return
      end

      -- Handle command_result
      if resp.event == "command_result" then
        handle_command_result(resp)
        -- If the command produced no output, just end stream
        -- (session_create/switch commands don't produce text output)
        ui.end_assistant_stream()
      elseif resp.event == "session_switch" or resp.event == "session_create" then
        -- Backward compat: old-style events from text command path
        if resp.event == "session_switch" and resp.data and resp.data.messages then
          ui.clear()
          ui.load_history(resp.data.messages)
        elseif resp.event == "session_create" then
          ui.clear()
        end
        if resp.session_id then
          session_id = resp.session_id
          ui.set_session_id(session_id)
        end
        -- Read the text reply frame
      elseif resp.output then
        ui.append_delta(resp.output)
        ui.end_assistant_stream()
      else
        ui.end_assistant_stream()
      end
    end

    client.execute(config.values.addr, session_id, json_cmd.cmd, json_cmd.params, on_command_response)
    return
  end

  -- Regular chat message — send as input
  pending = true

  -- Client-side intercept to clear active session if deleted
  local trimmed = vim.trim(text)
  if trimmed:match("^/session%s+delete%s*(.*)$") then
    local target = trimmed:match("^/session%s+delete%s*(.*)$")
    if target == session_id or target == "" or target == "this" then
      session_id = nil
      session_name = nil
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
  client.stream(config.values.addr, payload, function(frame)
    -- Handle command_result events (server may still send them for text commands)
    if frame.event == "command_result" then
      handle_command_result(frame)
      return
    end

    if frame.event == "session_switch" or frame.event == "session_create" then
      ui.clear()
      if frame.data and frame.data.messages then
        ui.load_history(frame.data.messages)
      end
    end

    -- Handle session_renamed events (from auto-rename or session_rename tool)
    if frame.event == "session_renamed" and frame.data then
      session_id = frame.data.session_id or session_id
      session_name = frame.data.name
      ui.set_session_id(session_id)
      -- Update window title to reflect new name
      ui.set_session_id(session_id)
      return
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
      -- Use exec_snippet for display (args is structured JSON, not shown directly)
      if frame.status == "start" or frame.status == "ok" or frame.status == "err" then
        ui.append_tool_event(frame.tool or "?", frame.status, frame.exec_snippet, frame.data)
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
    if _cancelling then
      _cancelling = false
      return
    end
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
    send_init()
  end
end

function M.send_oneshot(text)
  -- Try JSON command first
  local json_cmd = parse_slash_command(text)
  if json_cmd then
    client.execute(config.values.addr, session_id, json_cmd.cmd, json_cmd.params, function(err, resp)
      if err then
        vim.notify("hakka: " .. err, vim.log.levels.ERROR)
        return
      end
      if resp.error and resp.error ~= "" then
        vim.notify("hakka: " .. resp.error, vim.log.levels.ERROR)
        return
      end
      if resp.event == "command_result" and resp.data then
        -- Render structured data as text
        local text_out = ""
        if resp.data.sessions then
          for _, s in ipairs(resp.data.sessions) do
            local mark = s.current and "* " or "  "
            text_out = text_out .. mark .. (s.name or s.short_id or s.id) .. "\n"
          end
        elseif resp.data.session then
          text_out = resp.data.session.name or resp.data.session.id or ""
        elseif resp.data.models then
          for _, m in ipairs(resp.data.models) do
            local mark = m.current and "* " or "  "
            text_out = text_out .. mark .. m.name .. "\n"
          end
        elseif resp.data.tools then
          for _, t in ipairs(resp.data.tools) do
            local status = t.enabled and "[on]" or "[off]"
            text_out = text_out .. t.name .. " " .. status .. "\n"
          end
        end
        if text_out ~= "" then
          vim.notify(text_out:gsub("%s+$", ""), vim.log.levels.INFO)
        end
      end
      if resp.session_id then
        session_id = resp.session_id
        ui.set_session_id(session_id)
      end
    end)
    return
  end

  -- Regular chat
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
  session_name = nil
  ui.set_session_id(nil)
  ui.set_model(nil)
  vim.notify("hakka: session reset", vim.log.levels.INFO)
end

function M.cancel()
  if not pending then
    vim.notify("hakka: no active request to cancel", vim.log.levels.INFO)
    return
  end
  pending = false
  _cancelling = true
  ui.end_assistant_stream()
  ui.append_assistant("_[cancelled]_")

  if not session_id then
    _cancelling = false
    return
  end

  local payload = {
    type = "cancel",
    session_id = session_id,
  }
  client.send(config.values.addr, payload, function(err, resp)
    _cancelling = false
    if err then
      vim.notify("hakka: cancel error: " .. err, vim.log.levels.ERROR)
      return
    end
  end)
end

return M
