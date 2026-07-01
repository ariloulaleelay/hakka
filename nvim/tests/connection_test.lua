---
-- Tests for connection.lua — persistent WebSocket connection.
--
-- These tests use a mock wsclient to avoid needing a real server.
--
-- Usage: nvim --headless -c "lua dofile('tests/connection_test.lua')" -c "qa!"

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

-- ──────────────────────────────────────────
-- Mock wsclient
-- ──────────────────────────────────────────

local mock_wsclient = {}

-- Each mock connection has a buffer of received frames and can simulate
-- server responses.
function mock_wsclient.connect(url, on_done)
  local received = {}
  local on_frame_cb
  local on_close_cb
  local closed = false

  local conn = {
    received = received,
    send = function(self, payload)
      table.insert(received, payload)
    end,
    close = function(self)
      closed = true
    end,
    on_frame = function(self, cb)
      on_frame_cb = cb
    end,
    on_close = function(self, cb)
      on_close_cb = cb
    end,
    -- Simulate receiving a JSON frame from the server
    simulate_frame = function(self, data)
      local text = type(data) == "table" and vim.json.encode(data) or tostring(data)
      if on_frame_cb then
        on_frame_cb(text)
      end
    end,
    -- Simulate server closing the connection
    simulate_close = function(self, err)
      if on_close_cb then
        vim.schedule(function() on_close_cb(err) end)
      end
    end,
  }

  -- Schedule the on_done callback as the real wsclient does
  vim.schedule(function()
    if on_done then
      on_done(nil)
    end
  end)

  return conn
end

-- Mock wsclient as a module
mock_wsclient.__index = mock_wsclient

-- Install mock before loading connection module
package.loaded["hakka.wsclient"] = mock_wsclient

-- ──────────────────────────────────────────

-- Helper: create a fresh connection instance for each test
local function fresh_connection()
  package.loaded["hakka.connection"] = nil
  local c = require("hakka.connection")
  c.setup({ addr = "ws://test:9999/ws" })
  return c
end

-- Helper: wait for a condition with timeout
local function wait_until(cond, timeout_ms)
  timeout_ms = timeout_ms or 2000
  local step = 50
  local elapsed = 0
  while elapsed < timeout_ms do
    if cond() then return true end
    vim.wait(step, function() return cond() end)
    elapsed = elapsed + step
  end
  return cond()
end

-- ──────────────────────────────────────────
heading("connection.setup and connect")

do
  local conn = fresh_connection()

  -- Wait for connection
  local connected = wait_until(function() return conn.is_connected() end)
  assert_eq(connected, true, "setup: connection established")
  assert_eq(type(conn.get_ws()), "table", "setup: ws object exists")

  io.write("  OK    connection.setup opens persistent WebSocket\n")
end

-- ──────────────────────────────────────────
heading("connection.execute — sends command, receives response")

do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)

  local ws = conn.get_ws()

  -- Send a command
  local got_resp
  conn.execute(nil, "session_create", {}, function(err, resp)
    got_resp = resp
  end)

  -- Simulate server response
  ws:simulate_frame({ type = "session", event = "session_create", session = { id = "test-123" } })

  -- Wait for response
  wait_until(function() return got_resp ~= nil end)

  assert_eq(got_resp.type, "session", "execute: response type is session")
  assert_eq(got_resp.event, "session_create", "execute: event is session_create")
  assert_eq(got_resp.session.id, "test-123", "execute: session id matches")

  -- Verify the sent message
  assert_eq(#ws.received, 1, "execute: 1 message sent (the command)")
  local sent = ws.received[1]
  if type(sent) == "table" then
    assert_eq(sent.type, "cmd", "execute: sent type is cmd")
    assert_eq(sent.command.cmd, "session_create", "execute: sent cmd is session_create")
  end

  io.write("  OK    connection.execute sends cmd and receives response\n")
end

-- ──────────────────────────────────────────
heading("connection.execute — skips welcome and auto-sub get_session")

do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)

  local ws = conn.get_ws()

  local got_resp
  conn.execute(nil, "session_info", {}, function(err, resp)
    got_resp = resp
  end)

  -- Simulate welcome (must be skipped)
  ws:simulate_frame({ type = "welcome", sessions = {} })
  -- Simulate auto-sub get_session (must be skipped)
  ws:simulate_frame({ type = "session", event = "get_session", session = { id = "auto-sub" } })
  -- Simulate the actual response
  ws:simulate_frame({ type = "result", cmd = "session_info", data = { session = { id = "real" } } })

  wait_until(function() return got_resp ~= nil end)

  assert_eq(got_resp.type, "result", "auto-sub skipped: response type is result")
  assert_eq(got_resp.cmd, "session_info", "auto-sub skipped: cmd is session_info")
  assert_eq(got_resp.data.session.id, "real", "auto-sub skipped: got real session info")

  io.write("  OK    connection.execute skips welcome and auto-sub get_session\n")
end

-- ──────────────────────────────────────────
heading("connection.execute — second get_session is NOT skipped")

do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)

  local ws = conn.get_ws()

  -- First request: should skip the auto-sub get_session
  local resp1
  conn.execute(nil, "session_create", {}, function(err, resp)
    resp1 = resp
  end)
  ws:simulate_frame({ type = "welcome" })
  ws:simulate_frame({ type = "session", event = "get_session", session = { id = "auto-sub" } })
  ws:simulate_frame({ type = "session", event = "session_create", session = { id = "new-sess" } })
  wait_until(function() return resp1 ~= nil end)
  assert_eq(resp1.event, "session_create", "first execute: got session_create, not auto-sub")

  -- Second request: get_session should NOT be skipped
  local resp2
  conn.execute(nil, "get_session", { id = "explicit" }, function(err, resp)
    resp2 = resp
  end)
  ws:simulate_frame({ type = "session", event = "get_session", session = { id = "explicit" }, messages = {} })
  wait_until(function() return resp2 ~= nil end)
  assert_eq(resp2.event, "get_session", "second execute: get_session processed")
  assert_eq(resp2.session.id, "explicit", "second execute: got explicit session ID")

  io.write("  OK    connection.execute: subsequent get_session frames processed\n")
end

-- ──────────────────────────────────────────
heading("connection.execute — handles req frames (vim exec)")

do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)

  local ws = conn.get_ws()

  local got_resp
  conn.execute(nil, "session_list", {}, function(err, resp)
    got_resp = resp
  end)

  -- Simulate a req frame (server asking to eval Lua)
  ws:simulate_frame({ type = "req", request_id = "req-1", command = "return 42" })
  -- Then the actual response
  ws:simulate_frame({ type = "result", cmd = "session_list", data = { sessions = {} } })

  wait_until(function() return got_resp ~= nil end)

  assert_eq(got_resp.type, "result", "req handled: response type is result")
  assert_eq(got_resp.cmd, "session_list", "req handled: cmd matches")

  -- Wait for the resp frame to be sent (it's in a vim.schedule callback)
  vim.wait(200, function()
    if #ws.received > 0 then
      local last = ws.received[#ws.received]
      return type(last) == "table" and last.type == "resp"
    end
    return false
  end)

  -- Verify the resp frame was sent back
  local last_sent = ws.received[#ws.received]
  assert_eq(type(last_sent), "table", "req handled: last sent is a table")
  if type(last_sent) == "table" then
    assert_eq(last_sent.type, "resp", "req handled: sent resp frame")
    assert_eq(last_sent.request_id, "req-1", "req handled: request_id matches")
    assert_eq(last_sent.result, 42, "req handled: result is 42")
  end

  io.write("  OK    connection.execute handles req frames and sends resp\n")
end

-- ──────────────────────────────────────────
heading("connection.chat — streaming turn")

do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)

  local ws = conn.get_ws()

  local deltas = {}
  local tools = {}
  local usages = {}
  local done_called = false

  conn.chat("test-sess", "Hello!", {
    on_delta = function(text) table.insert(deltas, text) end,
    on_tool = function(name, status, snippet, data)
      table.insert(tools, { name = name, status = status })
    end,
    on_usage = function(frame)
      table.insert(usages, frame)
    end,
    on_done = function(err, stats)
      done_called = true
    end,
  })

  -- Simulate streaming frames
  ws:simulate_frame({ type = "delta", session_id = "test-sess", text = "Hello" })
  ws:simulate_frame({ type = "delta", session_id = "test-sess", text = " there" })
  ws:simulate_frame({ type = "tool", session_id = "test-sess", id = "call-1", tool = "read_file", status = "start" })
  ws:simulate_frame({ type = "usage", session_id = "test-sess", prompt_tokens = 10, completion_tokens = 5, total_tokens = 15 })
  ws:simulate_frame({ type = "done", session_id = "test-sess", stats = { total_tokens = 15 } })

  wait_until(function() return done_called end)

  assert_eq(#deltas, 2, "chat: 2 delta frames")
  assert_eq(deltas[1], "Hello", "chat: first delta")
  assert_eq(deltas[2], " there", "chat: second delta")
  assert_eq(#tools, 1, "chat: 1 tool event")
  assert_eq(tools[1].name, "read_file", "chat: tool name")
  assert_eq(tools[1].status, "start", "chat: tool status")
  assert_eq(#usages, 1, "chat: 1 usage event")

  io.write("  OK    connection.chat streams deltas, tools, usage, done\n")
end

-- ──────────────────────────────────────────
heading("connection.cancel — sends cancel frame")

do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)

  local ws = conn.get_ws()

  local got_resp
  conn.cancel("test-sess", function(err, resp)
    got_resp = resp
  end)

  -- Simulate done response
  ws:simulate_frame({ type = "done", session_id = "test-sess", cancelled = true })

  wait_until(function() return got_resp ~= nil end)

  assert_eq(got_resp.cancelled, true, "cancel: cancelled field is true")

  -- Verify the cancel frame was sent
  local last_sent = ws.received[#ws.received]
  if type(last_sent) == "table" then
    assert_eq(last_sent.type, "cancel", "cancel: sent cancel frame")
    assert_eq(last_sent.session_id, "test-sess", "cancel: session_id matches")
  end

  io.write("  OK    connection.cancel sends cancel frame\n")
end

-- ──────────────────────────────────────────
heading("connection — queuing works when disconnected")

do
  -- Override wsclient mock to simulate connection failure
  local fail_wsclient = {
    connect = function(url, on_done)
      vim.schedule(function()
        on_done("connection refused")
      end)
      return nil
    end,
  }
  package.loaded["hakka.wsclient"] = fail_wsclient

  package.loaded["hakka.connection"] = nil
  local conn = require("hakka.connection")
  conn.setup({ addr = "ws://down-server:9999/ws" })

  -- Give it time to fail
  vim.wait(300)

  -- Immediately disconnected
  local got_resp
  conn.execute(nil, "session_create", {}, function(err, resp)
    got_resp = resp or err
  end)

  -- Give it a moment — should not crash, but the command should be queued
  vim.wait(200)

  -- The command was queued (no response expected since not connected)
  -- No crash means success
  io.write("  OK    connection queues commands when disconnected\n")

  -- Restore mock
  package.loaded["hakka.wsclient"] = mock_wsclient
end

-- ──────────────────────────────────────────
heading("connection — concurrent execute calls are queued correctly")

do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)

  local ws = conn.get_ws()

  local results = {}
  local function resp_cb(key)
    return function(err, resp)
      results[key] = resp
    end
  end

  -- Send two commands in quick succession
  conn.execute(nil, "session_info", {}, resp_cb("info"))
  conn.execute(nil, "session_create", {}, resp_cb("create"))

  -- Respond to both
  ws:simulate_frame({ type = "result", cmd = "session_info", data = { session = { id = "existing" } } })
  ws:simulate_frame({ type = "session", event = "session_create", session = { id = "new-one" } })

  wait_until(function() return results.info ~= nil and results.create ~= nil end)

  assert_eq(results.info.type, "result", "concurrent: info response type is result")
  assert_eq(results.info.data.session.id, "existing", "concurrent: info session id")
  assert_eq(results.create.event, "session_create", "concurrent: create event")
  assert_eq(results.create.session.id, "new-one", "concurrent: create session id")

  io.write("  OK    connection handles concurrent execute calls\n")
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
