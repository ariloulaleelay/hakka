--- Hakka Neovim plugin — v2 protocol.
---
--- Thin coordinator that wires connection, session, and UI together.
---
--- Frame type handling:
---   Inbound:  welcome, delta, done, tool, usage, req, result, session, error
---   Outbound: chat, cmd, resp, cancel

local config = require("hakka.config")
local connection = require("hakka.connection")
local session = require("hakka.session")
local ui = require("hakka.ui")

local M = {}

local pending = false
local _cancelling = false

function M.setup(opts)
  config.setup(opts)
  connection.setup(opts)
  session.setup({ connection = connection })
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

  -- Normalize combined slash commands like /session_list → /session list
  local known_prefixes = { "/session", "/model", "/tool", "/models" }
  for _, prefix in ipairs(known_prefixes) do
    if cmd:match("^" .. prefix .. "_") then
      local suffix = cmd:sub(#prefix + 2)
      local new_parts = { prefix, suffix }
      for i = 2, #parts do
        table.insert(new_parts, parts[i])
      end
      parts = new_parts
      cmd = prefix
      break
    end
  end

  -- /help
  if cmd == "/help" then
    return { cmd = "help", params = {} }
  end

  -- /continue
  if cmd == "/continue" then
    return { cmd = "continue", params = {} }
  end

  -- /start
  if cmd == "/start" then
    return { cmd = "start", params = {} }
  end

  -- /cwd_set <path>
  if cmd == "/cwd_set" then
    local path = table.concat(parts, " ", 2)
    if path and path ~= "" then
      return { cmd = "cwd_set", params = { cwd = path } }
    end
    return nil
  end

  -- /compact [n]
  if cmd == "/compact" then
    local n = tonumber(parts[2])
    return { cmd = "compact", params = n and { n = n } or {} }
  end

  -- /session list | create | info | autorename | delete | get | rename
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
    elseif sub == "get" or sub == "switch" then
      local target = parts[3] or ""
      return { cmd = "get_session", params = { id = target } }
    elseif sub == "rename" then
      local name_parts = {}
      for i = 3, #parts do
        table.insert(name_parts, parts[i])
      end
      local name = table.concat(name_parts, " ")
      return { cmd = "session_rename", params = { name = name } }
    end
    return nil
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
      return { cmd = "session_info", params = {} }
    elseif sub == "switch" or sub == "get" then
      local name = parts[3]
      if name then
        return { cmd = "model_switch", params = { name = name } }
      end
    end
    if sub and sub ~= "list" and sub ~= "show" and sub ~= "get" then
      return { cmd = "model_switch", params = { name = sub } }
    end
    return { cmd = "session_info", params = {} }
  end

  -- /tool list | allow | deny
  if cmd == "/tool" then
    local sub = parts[2]
    if sub == "list" then
      return { cmd = "tool_list", params = {} }
    elseif sub == "enable" or sub == "allow" then
      local name = parts[3]
      if name then
        return { cmd = "tool_allow", params = { name = name } }
      end
    elseif sub == "disable" or sub == "deny" then
      local name = parts[3]
      if name then
        return { cmd = "tool_deny", params = { name = name } }
      end
    end
    return nil
  end

  return nil
end

--- Handle a "result" frame (from a JSON command).
local function handle_result(frame)
  local cmd = frame.cmd
  local data = frame.data or {}

  if cmd == "session_create" or cmd == "start" then
    session.set_from_response(frame)
    ui.set_session_id(session.current() and session.current().id)
    if session.current() and session.current().model then
      ui.set_model(session.current().model)
    end
    ui.clear()
  elseif cmd == "get_session" then
    session.set_from_response(frame)
    ui.set_session_id(session.current() and session.current().id)
    if data.messages then
      if ui.is_open() then
        ui.clear()
        ui.load_history(data.messages)
      end
    else
      ui.clear()
    end
  elseif cmd == "session_delete" then
    if data.active_cleared then
      session.reset()
      ui.set_session_id(nil)
      ui.clear()
      ui.append_delta("Session " .. (data.deleted or "?") .. " deleted (active session cleared)")
    elseif data.deleted then
      ui.append_delta("Session " .. data.deleted .. " deleted")
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
      ui.append_delta(table.concat(lines, "\n"))
    end
  elseif cmd == "session_rename" then
    if data.session then
      session.set_from_response({ type = "result", cmd = "session_rename", data = data })
      ui.set_session_id(session.current() and session.current().id)
      ui.append_delta("Session renamed to: " .. (data.session.name or ""))
    end
  elseif cmd == "session_autorename" then
    if data.session and data.session.name then
      session.set_from_response({ type = "result", cmd = "session_autorename", data = data })
      ui.set_session_id(session.current() and session.current().id)
      ui.append_delta("Session renamed to: " .. data.session.name)
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
      ui.append_delta(table.concat(lines, "\n"))
    else
      ui.append_delta("No sessions found.")
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
      ui.append_delta(table.concat(lines, "\n"))
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
      ui.append_delta(table.concat(lines, "\n"))
    end
  elseif cmd == "tool_allow" then
    if data.allowed then
      ui.append_delta("allowed: " .. table.concat(data.allowed, ", "))
    end
  elseif cmd == "tool_deny" then
    if data.denied then
      ui.append_delta("denied: " .. table.concat(data.denied, ", "))
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
      ui.append_delta(table.concat(lines, "\n"))
    end
  elseif cmd == "compact" then
    if data.compact_soft_limit then
      ui.append_delta("compact soft limit: " .. data.compact_soft_limit)
    end
  end
end

--- Handle a "session" frame (session lifecycle events).
local function handle_session_event(frame)
  local ev = frame.event
  local sid = frame.session and frame.session.id

  if ev == "session_create" or ev == "created" then
    session.set_from_response(frame)
    ui.set_session_id(session.current() and session.current().id)
    if frame.session and frame.session.model then
      ui.set_model(frame.session.model)
    end
    ui.clear()
  elseif ev == "get_session" then
    session.set_from_response(frame)
    ui.set_session_id(session.current() and session.current().id)
    if frame.messages then
      if ui.is_open() then
        ui.clear()
        ui.load_history(frame.messages)
      end
    else
      ui.clear()
    end
  elseif ev == "renamed" then
    if sid then
      ui.set_session_id(sid)
    end
  elseif ev == "deleted" then
    if frame.session == nil then
      session.reset()
      ui.set_session_id(nil)
    end
  end
end

--- Send a user message or slash command.
--- This is passed as the on_send callback to ui.open().
--- @param text string The user's input
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

    connection.execute(session.current() and session.current().id,
      json_cmd.cmd, json_cmd.params,
      function(err, resp)
        pending = false
        if err then
          ui.append_error(err)
          return
        end
        if not resp then
          ui.end_assistant_stream()
          return
        end
        if resp.error and resp.error ~= "" then
          ui.append_error(resp.error)
          return
        end

        if resp.type == "result" then
          handle_result(resp)
          ui.end_assistant_stream()
        elseif resp.type == "session" then
          handle_session_event(resp)
          ui.end_assistant_stream()
        elseif resp.type == "done" then
          if resp.text then
            ui.append_delta(resp.text)
          end
          ui.end_assistant_stream()
        else
          ui.end_assistant_stream()
        end
      end)
    return

  -- Any text starting with / but not recognized — intercept locally
  elseif vim.trim(text):match("^/") then
    ui.append_user(text)
    ui.begin_assistant_stream()
    ui.append_delta("Unknown command: " .. vim.trim(text):match("^/(%S+)"))
    pending = false
    ui.end_assistant_stream()
    return
  end

  -- Regular chat message — ensure we have a session, then send
  pending = true
  ui.append_user(text)
  ui.begin_assistant_stream()

  session.ensure(function(err, session_id)
    if err then
      pending = false
      ui.append_error("session: " .. err)
      ui.end_assistant_stream()
      return
    end

    if session.current() and session.current().model then
      ui.set_model(session.current().model)
    end
    if session_id then
      ui.set_session_id(session_id)
    end

    local saw_error = false
    connection.chat(session_id, text, {
      on_delta = function(delta_text)
        ui.append_delta(delta_text)
      end,
      on_tool = function(name, status, snippet, data)
        ui.append_tool_event(name, status, snippet, data)
      end,
      on_usage = function(usage_frame)
        ui.append_usage_event(usage_frame)
      end,
      on_done = function(err_text, stats)
        pending = false
        if _cancelling then
          _cancelling = false
          return
        end
        if err_text and not saw_error then
          ui.append_error(err_text)
          return
        end
        ui.end_assistant_stream()
      end,
    })
  end)
end

function M.toggle()
  if ui.is_open() then
    ui.close()
  else
    ui.open(send, M.cancel)
  end
end

function M.send_oneshot(text)
  -- Try JSON command first
  local json_cmd = parse_slash_command(text)
  if json_cmd then
    connection.execute(session.current() and session.current().id,
      json_cmd.cmd, json_cmd.params,
      function(err, resp)
        if err then
          vim.notify("hakka: " .. err, vim.log.levels.ERROR)
          return
        end
        if not resp then return end
        if resp.error and resp.error ~= "" then
          vim.notify("hakka: " .. resp.error, vim.log.levels.ERROR)
          return
        end

        -- Format the response for one-shot display
        if resp.type == "result" and resp.data then
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
        elseif resp.type == "session" and resp.session then
          session.set_from_response(resp)
          ui.set_session_id(session.current() and session.current().id)
          vim.notify("session: " .. (resp.session.name or resp.session.id or ""), vim.log.levels.INFO)
        elseif resp.type == "done" and resp.text and resp.text ~= "" then
          vim.notify(resp.text, vim.log.levels.INFO)
        end
      end)
    return

  -- Any text starting with / but not recognized
  elseif vim.trim(text):match("^/") then
    vim.notify("hakka: unknown command: " .. vim.trim(text):match("^/(%S+)"), vim.log.levels.WARN)
    return
  end

  -- Regular chat — ensure session, then send non-streaming
  session.ensure(function(err, session_id)
    if err then
      vim.notify("hakka: " .. err, vim.log.levels.ERROR)
      return
    end
    if session.current() and session.current().model then
      ui.set_model(session.current().model)
    end
    if session_id then
      ui.set_session_id(session_id)
    end

    local payload = {
      type = "chat",
      session_id = session_id,
      input = text,
    }
    connection.execute(session_id, "chat_direct", { input = text }, function(err, resp)
      if err then
        vim.notify("hakka: " .. err, vim.log.levels.ERROR)
        return
      end
      if not resp then return end
      if resp.error and resp.error ~= "" then
        vim.notify("hakka: " .. resp.error, vim.log.levels.ERROR)
        return
      end
      vim.notify(resp.text or "", vim.log.levels.INFO)
    end)
  end)
end

function M.reset()
  session.reset()
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

  local sid = session.current() and session.current().id
  if not sid then
    _cancelling = false
    return
  end

  connection.cancel(sid, function(err, resp)
    _cancelling = false
    if err then
      vim.notify("hakka: cancel error: " .. err, vim.log.levels.ERROR)
    end
  end)
end

return M
