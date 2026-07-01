---
--- Tests for active session filtering in connection.lua.
---
--- Verifies that when frames arrive from a DIFFERENT session than the
--- one the user is chatting with, they are NOT routed to the active
--- turn handler or pending commands.
---
--- Bug scenario: User has session A streaming in N1, opens N2 and
--- starts chatting in session B. Session A's streaming frames leak
--- into N2's chat UI because the client doesn't filter by session_id.
---
--- Usage: nvim --headless -c "lua dofile('tests/active_session_test.lua')" -c "qa!"

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

local function heading(name)
  io.write(string.format("\n--- %s ---\n", name))
end

--- Bootstrap: ensure lua/hakka is on the package path
local root = vim.fn.fnamemodify(vim.fn.getcwd(), ":p")
local lua_path = root .. "lua/?.lua"
package.path = lua_path .. ";" .. package.path

--- Mock wsclient
local mock_wsclient = {}

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
    simulate_frame = function(self, data)
      local text = type(data) == "table" and vim.json.encode(data) or tostring(data)
      if on_frame_cb then
        on_frame_cb(text)
      end
    end,
    simulate_close = function(self, err)
      if on_close_cb then
        vim.schedule(function() on_close_cb(err) end)
      end
    end,
  }

  vim.schedule(function()
    if on_done then on_done(nil) end
  end)

  return conn
end

package.loaded["hakka.wsclient"] = mock_wsclient

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

local function fresh_connection()
  package.loaded["hakka.connection"] = nil
  local c = require("hakka.connection")
  c.setup({ addr = "ws://test:9999/ws" })
  return c
end

-- ──────────────────────────────────────────
heading("active_turn filters by session_id — deltas from other session dropped")

-- User is chatting in session "my-session". Frames arrive from
-- "other-session" (a streaming turn in another client). They must NOT
-- be delivered to the active_turn handler.
do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)
  local ws = conn.get_ws()

  local deltas = {}
  local tools = {}
  local usages = {}
  local done_called = false

  -- Start a chat in "my-session"
  conn.chat("my-session", "Hello!", {
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

  -- Simulate frames from a DIFFERENT session's active turn leaking in
  ws:simulate_frame({ type = "delta", session_id = "other-session", text = "leaked delta" })
  ws:simulate_frame({ type = "tool",  session_id = "other-session", id = "call-1", tool = "read_file", status = "start", snippet = "read_file 'foo'" })
  ws:simulate_frame({ type = "usage", session_id = "other-session", prompt_tokens = 10, completion_tokens = 5, total_tokens = 15 })
  ws:simulate_frame({ type = "done",  session_id = "other-session", stats = { total_tokens = 15 } })

  -- Small wait to ensure frames are processed
  vim.wait(200)

  -- None of these frames should have been routed to active_turn
  assert_eq(#deltas, 0, "other-session: 0 delta frames delivered to active_turn")
  assert_eq(#tools, 0, "other-session: 0 tool events delivered to active_turn")
  assert_eq(#usages, 0, "other-session: 0 usage events delivered to active_turn")
  assert_eq(done_called, false, "other-session: done NOT called on active_turn")

  io.write("  OK    active_turn filters out frames from other sessions\n")
end

-- ──────────────────────────────────────────
heading("active_turn accepts frames from matching session")

-- Same session's frames should still be delivered correctly.
do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)
  local ws = conn.get_ws()

  local deltas = {}
  local done_called = false

  conn.chat("my-session", "Hello!", {
    on_delta = function(text) table.insert(deltas, text) end,
    on_done = function(err, stats) done_called = true end,
  })

  -- Simulate frames from the SAME session (should be delivered)
  ws:simulate_frame({ type = "delta", session_id = "my-session", text = "hello" })
  ws:simulate_frame({ type = "delta", session_id = "my-session", text = " there" })
  ws:simulate_frame({ type = "done",  session_id = "my-session", stats = {} })

  wait_until(function() return done_called end)

  assert_eq(#deltas, 2, "my-session: 2 delta frames delivered")
  assert_eq(deltas[1], "hello", "my-session: first delta correct")
  assert_eq(deltas[2], " there", "my-session: second delta correct")

  io.write("  OK    active_turn delivers frames from matching session\n")
end

-- ──────────────────────────────────────────
heading("active_turn with missing session_id — rejects unlabeled frames")

-- If a frame has no session_id at all (shouldn't happen with proper
-- server, but be safe), it should NOT be routed to active_turn.
do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)
  local ws = conn.get_ws()

  local deltas = {}
  local done_called = false

  conn.chat("my-session", "Hello!", {
    on_delta = function(text) table.insert(deltas, text) end,
    on_done = function(err, stats) done_called = true end,
  })

  -- Frame WITHOUT session_id — should be rejected
  ws:simulate_frame({ type = "delta", text = "no-session leak" })
  vim.wait(200)

  assert_eq(#deltas, 0, "missing session_id: 0 deltas delivered")

  io.write("  OK    active_turn rejects frames without session_id\n")
end

-- ──────────────────────────────────────────
heading("pending_commands with active_turn — other-session delta not routed to callback")

-- If a user sends a command while another session is streaming, the
-- streaming frames should NOT be routed as command responses.
do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)
  local ws = conn.get_ws()

  -- Register active_turn for one session
  local active_deltas = {}
  conn.chat("session-A", "Hello!", {
    on_delta = function(text) table.insert(active_deltas, text) end,
    on_done = function(err, stats) end,
  })

  -- Send a command (goes to pending_commands queue)
  local cmd_response = nil
  conn.execute(nil, "session_info", {}, function(err, resp)
    cmd_response = resp
  end)

  -- A delta frame from "other-session" arrives — should NOT be delivered
  -- as a command response.
  ws:simulate_frame({ type = "delta", session_id = "other-session", text = "should not be cmd response" })
  vim.wait(200)

  -- The command response should still be pending (not consumed by delta)
  assert_eq(cmd_response, nil, "pending command not consumed by other-session delta")

  -- Now deliver the real command response
  ws:simulate_frame({ type = "result", cmd = "session_info", data = { session = { id = "real" } } })
  wait_until(function() return cmd_response ~= nil end)

  assert_eq(cmd_response.type, "result", "pending command received correct response")
  assert_eq(cmd_response.cmd, "session_info", "pending command cmd is session_info")

  io.write("  OK    pending_commands not consumed by other-session frames\n")
end

-- ──────────────────────────────────────────
heading("pending_commands without active_turn — other-session delta not routed")

-- If there's NO active_turn, pending command should still NOT be
-- consumed by streaming frames from another session.
do
  local conn = fresh_connection()
  wait_until(function() return conn.is_connected() end)
  local ws = conn.get_ws()

  -- No active_turn. Just a pending command.
  local cmd_response = nil
  conn.execute(nil, "session_list", {}, function(err, resp)
    cmd_response = resp
  end)

  -- Streaming frames from any session arrive — should skip pending_commands
  ws:simulate_frame({ type = "delta", session_id = "any-session", text = "leak" })
  ws:simulate_frame({ type = "tool",  session_id = "any-session", id = "c1", tool = "read_file", status = "start" })
  ws:simulate_frame({ type = "usage", session_id = "any-session", prompt_tokens = 5, completion_tokens = 3, total_tokens = 8 })
  vim.wait(200)

  assert_eq(cmd_response, nil, "pending command not consumed by streaming frames")

  -- Now deliver the real response
  ws:simulate_frame({ type = "result", cmd = "session_list", data = { sessions = {} } })
  wait_until(function() return cmd_response ~= nil end)

  assert_eq(cmd_response.type, "result", "pending command got correct response")

  io.write("  OK    pending_commands (no active_turn) skips delta/tool/usage frames\n")
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
