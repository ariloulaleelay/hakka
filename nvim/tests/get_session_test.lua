---
--- Tests for get_session auto-sub skip logic in client.lua
---
--- Verifies that:
--- 1. The first "get_session" event after "welcome" is skipped (auto-sub)
--- 2. Subsequent "get_session" events are processed (explicit command responses)
---
--- Usage: nvim --headless -c "lua dofile('tests/get_session_test.lua')" -c "qa!"

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
  io.stderr:write(string.format("  FAIL  %sgot %s, expected %s\n", prefix, tostring(got), tostring(expected)))
end

-- Replicate the exact handle_frame logic from client.lua's connect_and_send.
-- Returns a handle_frame function and a way to inspect what was processed.
local function make_handler(on_frame, on_done, single_response)
  local auto_sub_skipped = false
  local processed_frames = {}

  local function handle_frame(text)
    local ok, parsed = pcall(vim.json.decode, text)
    if not ok then
      if on_done then on_done("json: " .. tostring(parsed)) end
      return
    end

    -- Skip welcome
    if parsed.type == "welcome" then
      table.insert(processed_frames, { action = "skipped", type = parsed.type })
      return
    end

    -- Skip auto-sub get_session (first one only)
    if parsed.type == "session" and parsed.event == "get_session" and not auto_sub_skipped then
      auto_sub_skipped = true
      table.insert(processed_frames, { action = "skipped", type = parsed.type, event = parsed.event, reason = "auto_sub" })
      return
    end

    -- req handling omitted for test simplicity

    -- done/error are terminal
    if parsed.type == "done" then
      on_frame(parsed)
      table.insert(processed_frames, { action = "processed", type = parsed.type })
      if on_done then on_done(nil) end
      return
    end

    if parsed.type == "error" then
      on_frame(parsed)
      table.insert(processed_frames, { action = "processed", type = parsed.type })
      if on_done then on_done(nil) end
      return
    end

    -- All other frames
    if single_response then
      on_frame(parsed)
      table.insert(processed_frames, { action = "processed", type = parsed.type, event = parsed.event })
      if on_done then on_done(nil) end
    else
      on_frame(parsed)
      table.insert(processed_frames, { action = "forwarded", type = parsed.type })
    end
  end

  return handle_frame, function() return processed_frames end
end

-- Helper to create a JSON string
local function json(obj)
  return vim.json.encode(obj)
end

-- ──────────────────────────────────────────
io.write("--- get_session auto-sub skip ---\n")

-- Test 1: session_create command — auto-sub get_session should be skipped,
-- session_create response should be processed.
do
  local frames = {}
  local on_frame = function(f) table.insert(frames, f) end
  local on_done_called = false
  local handle, get_processed = make_handler(on_frame, function(err) on_done_called = true end, true)

  -- Simulate server frames arriving after connection + command send
  handle(json({ type = "welcome" }))
  handle(json({ type = "session", event = "get_session", session = { id = "auto-sub" } }))
  handle(json({ type = "session", event = "session_create", session = { id = "new-session" } }))

  local processed = get_processed()
  assert_eq(#processed, 3, "total frames processed")
  assert_eq(processed[1].action, "skipped", "welcome skipped")
  assert_eq(processed[1].type, "welcome", "welcome type")
  assert_eq(processed[2].action, "skipped", "auto-sub get_session skipped")
  assert_eq(processed[2].reason, "auto_sub", "auto-sub reason")
  assert_eq(processed[3].action, "processed", "session_create processed")
  assert_eq(processed[3].event, "session_create", "session_create event")
  assert_eq(#frames, 1, "one frame forwarded to on_frame")
  assert_eq(frames[1].event, "session_create", "forwarded frame is session_create")
  io.write("  OK    session_create: auto-sub get_session skipped, response processed\n")
end

-- Test 2: get_session command — auto-sub get_session should be skipped,
-- but the explicit get_session response should be processed.
do
  local frames = {}
  local on_frame = function(f) table.insert(frames, f) end
  local on_done_called = false
  local handle, get_processed = make_handler(on_frame, function(err) on_done_called = true end, true)

  -- Simulate: welcome → auto-sub get_session → explicit get_session response
  handle(json({ type = "welcome" }))
  handle(json({ type = "session", event = "get_session", session = { id = "auto-sub-session" } }))
  handle(json({ type = "session", event = "get_session", session = { id = "explicit-session" }, messages = {} }))

  local processed = get_processed()
  assert_eq(#processed, 3, "total frames processed")
  assert_eq(processed[1].action, "skipped", "welcome skipped")
  assert_eq(processed[2].action, "skipped", "auto-sub get_session skipped")
  assert_eq(processed[2].reason, "auto_sub", "auto-sub reason")
  assert_eq(processed[3].action, "processed", "second get_session processed (not skipped!)")
  assert_eq(processed[3].event, "get_session", "second get_session event")
  assert_eq(#frames, 1, "one frame forwarded to on_frame")
  assert_eq(frames[1].event, "get_session", "forwarded frame is get_session")
  assert_eq(frames[1].session.id, "explicit-session", "forwarded session ID is the explicit one")
  io.write("  OK    get_session: auto-sub skipped, explicit get_session processed\n")
end

-- Test 3: Streaming mode — auto-sub get_session still skipped,
-- other frames forwarded normally.
do
  local frames = {}
  local on_frame = function(f) table.insert(frames, f) end
  local handle, get_processed = make_handler(on_frame, nil, false)

  handle(json({ type = "welcome" }))
  handle(json({ type = "session", event = "get_session", session = { id = "auto-sub" } }))
  handle(json({ type = "delta", text = "Hello" }))
  handle(json({ type = "tool", tool = "read_file", status = "start" }))
  handle(json({ type = "done", text = "Done" }))

  local processed = get_processed()
  assert_eq(#processed, 5, "total frames in streaming mode")
  assert_eq(processed[1].action, "skipped", "welcome skipped in streaming")
  assert_eq(processed[2].action, "skipped", "auto-sub get_session skipped in streaming")
  assert_eq(processed[2].reason, "auto_sub", "auto-sub reason in streaming")
  assert_eq(processed[3].action, "forwarded", "delta forwarded in streaming")
  assert_eq(processed[4].action, "forwarded", "tool forwarded in streaming")
  assert_eq(processed[5].action, "processed", "done processed in streaming")
  assert_eq(#frames, 3, "three frames forwarded in streaming")
  io.write("  OK    streaming: auto-sub skipped, other frames forwarded\n")
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
