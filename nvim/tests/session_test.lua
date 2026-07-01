---
-- Tests for session.lua — session state management.
--
-- Usage: nvim --headless -c "lua dofile('tests/session_test.lua')" -c "qa!"

local ok = true
local total = 0
local passed = 0
local failed = 0

local function assert_eq(got, expected, msg)
  total = total + 1
  if got == expected then
    passed = passed + 1
    return
  end
  ok = false
  failed = failed + 1
  local prefix = msg or ""
  if prefix ~= "" then prefix = prefix .. ": " end
  io.stderr:write(string.format("  FAIL  %sgot %q, expected %q\n", prefix, tostring(got), tostring(expected)))
end

local function assert_table_eq(got, expected, msg)
  total = total + 1
  if got == nil and expected == nil then
    passed = passed + 1
    return
  end
  if type(got) ~= type(expected) then
    ok = false; failed = failed + 1
    io.stderr:write(string.format("  FAIL  %sgot type %s, expected type %s\n", msg or "", type(got), type(expected)))
    return
  end
  local function deep_eq(a, b)
    if type(a) ~= "table" then return a == b end
    for k, v in pairs(a) do
      if not deep_eq(v, b[k]) then return false end
    end
    for k, v in pairs(b) do
      if not deep_eq(v, a[k]) then return false end
    end
    return true
  end
  if deep_eq(got, expected) then
    passed = passed + 1
    return
  end
  ok = false; failed = failed + 1
  local prefix = msg or ""
  if prefix ~= "" then prefix = prefix .. ": " end
  io.stderr:write(string.format("  FAIL  %sgot %s, expected %s\n", prefix, vim.inspect(got), vim.inspect(expected)))
end

local function heading(name)
  io.write(string.format("\n--- %s ---\n", name))
end

-- Bootstrap: ensure lua/hakka is on the package path
local root = vim.fn.fnamemodify(vim.fn.getcwd(), ":p")
local lua_path = root .. "lua/?.lua"
package.path = lua_path .. ";" .. package.path

-- Use a mock connection for testing
local function mock_connection()
  local executed = {}
  return {
    executed = executed,
    -- session.lua calls connection.execute(session_id, cmd, params, cb)
    -- using dot notation on a module table (no self argument).
    execute = function(session_id, cmd, params, cb)
      table.insert(executed, { cmd = cmd, params = params })
      if cmd == "session_create" then
        vim.schedule(function()
          cb(nil, { type = "session", event = "session_create", session = { id = "new-session-123", name = "", model = "gpt4" } })
        end)
      elseif cmd == "get_session" then
        local sid = (params or {}).id or "unknown"
        vim.schedule(function()
          cb(nil, { type = "session", event = "get_session", session = { id = sid, name = "Test Session", model = "gpt4" }, messages = {} })
        end)
      elseif cmd == "session_list" then
        vim.schedule(function()
          cb(nil, { type = "result", cmd = "session_list", data = { sessions = {
            { id = "sess-1", name = "Chat A", current = false },
            { id = "sess-2", name = "Chat B", current = false },
          }}})
        end)
      elseif cmd == "session_delete" then
        vim.schedule(function()
          cb(nil, { type = "result", cmd = "session_delete", data = { deleted = "sess-1", active_cleared = true } })
        end)
      else
        vim.schedule(function() cb("unexpected cmd: " .. cmd, nil) end)
      end
    end,
  }
end

-- ──────────────────────────────────────────
heading("session.ensure — creates session lazily")

do
  local conn = mock_connection()
  package.loaded["hakka.session"] = nil
  local session = require("hakka.session")
  session.setup({ connection = conn })

  -- Initially no session
  assert_eq(session.current(), nil, "no session before ensure")

  -- Call ensure
  local got_err, got_id
  session.ensure(function(err, id)
    got_err = err
    got_id = id
  end)

  vim.wait(1000, function() return got_id ~= nil end)

  assert_eq(got_err, nil, "ensure: no error")
  assert_eq(got_id, "new-session-123", "ensure: session ID matches")
  assert_eq(#conn.executed, 1, "ensure: exactly one execute call")
  assert_eq(conn.executed[1].cmd, "session_create", "ensure: calls session_create")
  assert_eq(session.current().id, "new-session-123", "ensure: current session set")

  io.write("  OK    session.ensure creates session on first call\n")
end

-- ──────────────────────────────────────────
heading("session.ensure — reuses existing session")

do
  local conn = mock_connection()
  package.loaded["hakka.session"] = nil
  local session = require("hakka.session")
  session.setup({ connection = conn })

  -- First ensure creates
  local first_id
  session.ensure(function(err, id) first_id = id end)
  vim.wait(1000, function() return first_id ~= nil end)

  -- Second ensure should reuse, not create new
  local second_id
  session.ensure(function(err, id) second_id = id end)
  vim.wait(1000, function() return second_id ~= nil end)

  assert_eq(first_id, second_id, "ensure reuses existing session ID")
  assert_eq(#conn.executed, 1, "ensure: only one session_create call")

  io.write("  OK    session.ensure reuses existing session\n")
end

-- ──────────────────────────────────────────
heading("session.reset — clears current session")

do
  local conn = mock_connection()
  package.loaded["hakka.session"] = nil
  local session = require("hakka.session")
  session.setup({ connection = conn })

  session.ensure(function() end)
  vim.wait(1000, function() return session.current() ~= nil end)

  session.reset()
  assert_eq(session.current(), nil, "reset clears current session")

  -- Next ensure creates a new session
  local new_id
  session.ensure(function(err, id) new_id = id end)
  vim.wait(1000, function() return new_id ~= nil end)

  assert_eq(new_id, "new-session-123", "reset + ensure creates new session")
  assert_eq(#conn.executed, 2, "after reset+ensure: two session_create calls")

  io.write("  OK    session.reset clears session, subsequent ensure creates new one\n")
end

-- ──────────────────────────────────────────
heading("session.switch — switches to another session")

do
  local conn = mock_connection()
  package.loaded["hakka.session"] = nil
  local session = require("hakka.session")
  session.setup({ connection = conn })

  -- Ensure we have a current session first
  session.ensure(function() end)
  vim.wait(1000, function() return session.current() ~= nil end)

  -- Switch to another session
  local switch_id, switch_err
  session.switch("sess-2", function(err, sid)
    switch_err = err
    switch_id = sid
  end)
  vim.wait(1000, function() return switch_id ~= nil end)

  assert_eq(switch_err, nil, "switch: no error")
  assert_eq(switch_id, "sess-2", "switch: returns target session ID")
  assert_eq(session.current().id, "sess-2", "switch: current session updated")
  assert_eq(session.current().name, "Test Session", "switch: session name set")

  io.write("  OK    session.switch changes current session\n")
end

-- ──────────────────────────────────────────
heading("session.list — fetches and caches session list")

do
  local conn = mock_connection()
  package.loaded["hakka.session"] = nil
  local session = require("hakka.session")
  session.setup({ connection = conn })

  local list_result
  session.list(function(sessions)
    list_result = sessions
  end)
  vim.wait(1000, function() return list_result ~= nil end)

  assert_eq(#list_result, 2, "list: returns 2 sessions")
  assert_eq(list_result[1].id, "sess-1", "list: first session id")
  assert_eq(list_result[2].id, "sess-2", "list: second session id")

  io.write("  OK    session.list fetches session list\n")
end

-- ──────────────────────────────────────────
heading("session.delete — deletes and resets if active")

do
  local conn = mock_connection()
  package.loaded["hakka.session"] = nil
  local session = require("hakka.session")
  session.setup({ connection = conn })

  session.ensure(function() end)
  vim.wait(1000, function() return session.current() ~= nil end)

  local delete_err, delete_msg
  session.delete("sess-1", function(err, msg)
    delete_err = err
    delete_msg = msg
  end)
  vim.wait(1000, function() return delete_msg ~= nil end)

  assert_eq(delete_err, nil, "delete: no error")
  assert_eq(#conn.executed, 2, "delete: session_create then session_delete")
  assert_eq(conn.executed[2].cmd, "session_delete", "delete: command is session_delete")

  io.write("  OK    session.delete sends delete command\n")
end

-- ──────────────────────────────────────────
heading("session.set_from_response — handles session frames")

do
  local conn = mock_connection()
  package.loaded["hakka.session"] = nil
  local session = require("hakka.session")
  session.setup({ connection = conn })

  -- Simulate a session_create event from server
  session.set_from_response({
    type = "session",
    event = "session_create",
    session = { id = "from-server-456", name = "Server Created", model = "claude" }
  })
  assert_eq(session.current().id, "from-server-456", "set_from_response: session ID")
  assert_eq(session.current().name, "Server Created", "set_from_response: session name")
  assert_eq(session.current().model, "claude", "set_from_response: session model")

  io.write("  OK    session.set_from_response processes session frames\n")
end

-- ──────────────────────────────────────────
heading("session.set_from_response — ignores non-session frames")

do
  local conn = mock_connection()
  package.loaded["hakka.session"] = nil
  local session = require("hakka.session")
  session.setup({ connection = conn })

  session.set_from_response({ type = "delta", text = "hello" })
  assert_eq(session.current(), nil, "set_from_response: delta frame ignored")

  session.set_from_response({ type = "result", cmd = "tool_list", data = {} })
  assert_eq(session.current(), nil, "set_from_response: result frame ignored")

  io.write("  OK    session.set_from_response ignores non-session frames\n")
end

-- ──────────────────────────────────────────
io.write("\n--- Summary ---\n")

if not ok then
  io.stderr:write(string.format("FAILED: %d passed, %d failed out of %d tests\n", passed, failed, total))
  vim.cmd("cquit")
else
  io.write(string.format("ALL PASSED: %d tests\n", total))
  vim.cmd("qall!")
end
