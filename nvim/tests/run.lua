---
-- Test runner for hakka.nvim pure Lua modules.
-- Usage: nvim --headless -c "lua dofile('tests/run.lua')" -c "qa!"
-- Or via Makefile: make test

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
  io.stderr:write(string.format("  FAIL  %sgot %q, expected %q\n", prefix, got, expected))
end

local function heading(name)
  io.write(string.format("\n--- %s ---\n", name))
end

-- Bootstrap: ensure lua/hakka is on the package path
local root = vim.fn.fnamemodify(vim.fn.getcwd(), ":p")
local lua_path = root .. "lua/?.lua"
package.path = lua_path .. ";" .. package.path

-- ──────────────────────────────────────────
heading("client.execute_vim_command")

-- execute_vim_command is a local function in client.lua, so we replicate
-- the same logic here for testing.
local function execute_vim_command(cmd)
  local fn, compile_err = load(cmd)
  if not fn then
    return nil, tostring(compile_err)
  end
  local ok, result = pcall(fn)
  -- Clean up any lingering hakka buffers from previous tests.
local function cleanup_hakka_buffers()
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, 2, false)
    if #lines >= 1 and lines[1] == "# Me" then
      pcall(vim.api.nvim_buf_delete, b, { force = true })
    end
  end
end

-- ──────────────────────────────────────────
heading("reset_prompt idempotent — no double # Me")

-- When reset_prompt is called twice in a row (e.g. from append_error
-- during streaming followed by end_assistant_stream), it MUST NOT
-- produce a second # Me prompt.
do
  cleanup_hakka_buffers()
  local function fresh_ui()
    package.loaded["hakka.ui"] = nil
    return require("hakka.ui")
  end

  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- Helper: get our test buffer lines
  local function get_buf_lines()
    for _, b in ipairs(vim.api.nvim_list_bufs()) do
      local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
      -- Buffer created by ui.open has "# Me" at line 1
      if #lines >= 1 and lines[1] == "# Me" then
        return lines
      end
    end
    return {}
  end

  local function count_me_in(lines)
    local count = 0
    for _, l in ipairs(lines) do
      if l == "# Me" then count = count + 1 end
    end
    return count
  end

  -- Simulate: append_error during streaming followed by end_assistant_stream.
  -- This is what happens when a frame.error arrives in the streaming callback:
  -- append_error calls reset_prompt, then on_done calls end_assistant_stream
  -- which calls reset_prompt again.
  ui.append_user("hello")
  ui.begin_assistant_stream()
  -- Now simulate a command error frame that comes mid-stream
  -- We call append_error which calls reset_prompt internally
  ui.append_error("some error")
  -- Then end_assistant_stream is called (from on_done callback) which would
  -- call reset_prompt again, causing double # Me without the fix.
  ui.end_assistant_stream()

  local lines = get_buf_lines()
  local me_count = count_me_in(lines)

  -- Expected: 1 # Me for the label (from append_user) + 1 # Me for the prompt
  -- Total: 2. If > 2, we have the double-# Me bug.
  assert_eq(me_count, 2,
    "append_error+end_assistant_stream: exactly 2 '# Me' (1 label + 1 prompt), got " .. me_count ..
    ", lines: " .. vim.inspect(lines))
end

-- ──────────────────────────────────────────
heading("reset_prompt idempotent — direct double call")

-- Directly calling reset_prompt twice should also be safe.
do
  cleanup_hakka_buffers()
  local function fresh_ui()
    package.loaded["hakka.ui"] = nil
    return require("hakka.ui")
  end

  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- Simulate a regular conversation turn
  ui.append_user("hello")
  ui.begin_assistant_stream()
  ui.append_delta("hi there")
  -- Call end_assistant_stream once
  ui.end_assistant_stream()

  -- Get lines and count # Me
  local function get_buf_lines()
    for _, b in ipairs(vim.api.nvim_list_bufs()) do
      local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
      if #lines >= 1 and lines[1] == "# Me" then
        return lines
      end
    end
    return {}
  end

  local function count_me_in(lines)
    local count = 0
    for _, l in ipairs(lines) do
      if l == "# Me" then count = count + 1 end
    end
    return count
  end

  local lines = get_buf_lines()
  local me_count = count_me_in(lines)
  -- After one turn: 1 # Me label + 1 # Me prompt = 2
  assert_eq(me_count, 2,
    "after one turn: exactly 2 '# Me', got " .. me_count)

  -- Now simulate a second end_assistant_stream call (bug: reset_prompt called again)
  -- Without the fix, this would add another # Me
  ui.end_assistant_stream()

  lines = get_buf_lines()
  me_count = count_me_in(lines)
  -- Still 2: the duplicate call should not add another # Me
  assert_eq(me_count, 2,
    "after second end_assistant_stream: still 2 '# Me', got " .. me_count ..
    ", lines: " .. vim.inspect(lines))
end

-- ──────────────────────────────────────────
heading("reset_prompt idempotent — multiple regular turns")

-- Even after multiple full conversation cycles, # Me count should be correct.
do
  cleanup_hakka_buffers()
  local function fresh_ui()
    package.loaded["hakka.ui"] = nil
    return require("hakka.ui")
  end

  local ui = fresh_ui()
  pcall(ui.open, function() end)

  local function get_buf_lines()
    for _, b in ipairs(vim.api.nvim_list_bufs()) do
      local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
      if #lines >= 1 and lines[1] == "# Me" then
        for _, l in ipairs(lines) do
          if l == "turn_A" or l == "turn_B" then return lines end
        end
      end
    end
    return {}
  end

  local function count_me_in(lines)
    local count = 0
    for _, l in ipairs(lines) do
      if l == "# Me" then count = count + 1 end
    end
    return count
  end

  -- Turn A
  ui.append_user("turn_A")
  ui.begin_assistant_stream()
  ui.append_delta("reply A")
  ui.end_assistant_stream()

  local lines = get_buf_lines()
  assert_eq(count_me_in(lines), 2, "after turn A: 2 # Me")

  -- Turn B
  ui.append_user("turn_B")
  ui.begin_assistant_stream()
  ui.append_delta("reply B")
  ui.end_assistant_stream()

  lines = get_buf_lines()
  assert_eq(count_me_in(lines), 3, "after turn B: 3 # Me (2 labels + 1 prompt)")

  -- Turn C
  ui.append_user("turn_C")
  ui.begin_assistant_stream()
  ui.append_delta("reply C")
  ui.end_assistant_stream()

  lines = get_buf_lines()
  assert_eq(count_me_in(lines), 4, "after turn C: 4 # Me (3 labels + 1 prompt)")
end

if not ok then
    return nil, tostring(result)
  end
  return result, nil
end

-- Basic expression evaluation
local r, e = execute_vim_command("return 1 + 1")
assert_eq(r, 2, "return 1 + 1")
assert_eq(e, nil, "no error on 1+1")

-- Return buffer lines (the original failing command)
r, e = execute_vim_command("return vim.api.nvim_buf_get_lines(0, 0, -1, false)")
assert_eq(type(r), "table",  "buf_get_lines returns a table")
assert_eq(e,       nil,      "buf_get_lines no error")
assert_eq(#r,      1,        "buf has 1 line (empty buffer)")

-- Statement with return
r, e = execute_vim_command("vim.api.nvim_buf_set_lines(0, 0, -1, false, {'hello'}); return 'ok'")
assert_eq(r, "ok",  "set_lines + return 'ok'")
assert_eq(e, nil,   "set_lines no error")
-- Verify the buffer was actually modified
local lines = vim.api.nvim_buf_get_lines(0, 0, -1, false)
assert_eq(lines[1], "hello", "buffer content was set")
-- Restore buffer
vim.api.nvim_buf_set_lines(0, 0, -1, false, {""})

-- Local variables
r, e = execute_vim_command("local x = 42; return x")
assert_eq(r, 42,    "local var + return")
assert_eq(e, nil,   "local var no error")

-- String result
r, e = execute_vim_command("return 'hello world'")
assert_eq(r, "hello world", "string return")
assert_eq(e, nil,           "string return no error")

-- Table result
r, e = execute_vim_command("return {a = 1, b = 2}")
assert_eq(type(r), "table", "table return type")
assert_eq(e,       nil,     "table return no error")
assert_eq(r.a,     1,       "table.a = 1")
assert_eq(r.b,     2,       "table.b = 2")

-- Nil result
r, e = execute_vim_command("return nil")
assert_eq(r, nil,  "return nil")
assert_eq(e, nil,  "return nil no error")

-- Boolean result
r, e = execute_vim_command("return true")
assert_eq(r, true, "return true")
assert_eq(e, nil,  "return true no error")

-- Syntax error
r, e = execute_vim_command("return }invalid syntax")
assert_eq(r, nil,                            "syntax error returns nil")
assert(type(e) == "string",                  "syntax error msg is a string")
assert_eq(e:match("unexpected symbol"), "unexpected symbol", "syntax error mentions 'unexpected symbol'")

-- Runtime error
r, e = execute_vim_command("error('boom')")
assert_eq(r, nil,         "runtime error returns nil")
assert_eq(e:match(": boom$"), ": boom", "runtime error message ends with ': boom'")

-- Runtime error 2: call nil
r, e = execute_vim_command("return vim.api.nonexistent_function()")
assert_eq(r, nil,                                       "nil function returns nil")
assert_eq(e:match("attempt to call"), "attempt to call", "nil func: 'attempt to call' in error")

-- File path via vim.fn
r, e = execute_vim_command("return vim.fn.expand('%:p')")
assert_eq(type(r), "string", "expand path returns string")
assert_eq(e,       nil,      "expand path no error")

-- Filetype
r, e = execute_vim_command("return vim.bo.filetype")
assert_eq(type(r), "string", "filetype returns string (may be empty)")
assert_eq(e,       nil,      "filetype no error")

-- ──────────────────────────────────────────
heading("util.shorten_snippet")

local util = require("hakka.util")

assert_eq(util.shorten_snippet(nil, 60),           "",                "nil input")
assert_eq(util.shorten_snippet("", 60),            "",                "empty string")
assert_eq(util.shorten_snippet("hello", 60),       "hello",           "short text unchanged")
assert_eq(util.shorten_snippet("a very long string that exceeds sixty characters by quite a bit", 60), "a very long string that exceeds sixty characters by quite...", "63 chars truncated to 60")
assert_eq(util.shorten_snippet("exactly 60 chars-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 60), "exactly 60 chars-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx...", "62 chars truncated to 60")
assert_eq(util.shorten_snippet("61 chars--xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 60), "61 chars--xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx...", "61 chars truncated to 60")
assert_eq(util.shorten_snippet("hello", 3),        "...",             "very short max_len produces just ...")
assert_eq(util.shorten_snippet("hello", 5),        "hello",           "exactly 5 chars fits")
assert_eq(util.shorten_snippet("hello!", 5),       "he...",           "6 chars trunc to 5")

assert_eq(util.escape_snippet(nil),          "",                "nil input")
assert_eq(util.escape_snippet(""),           "",                "empty string")
assert_eq(util.escape_snippet("hello"),      "hello",           "plain text unchanged")
assert_eq(util.escape_snippet("hello\nworld"), "hello\\nworld", "newline escaped")
assert_eq(util.escape_snippet("\n"),         "\\n",             "single newline")
assert_eq(util.escape_snippet("a\nb\nc"),    "a\\nb\\nc",       "multiple newlines")
assert_eq(util.escape_snippet("[test]"),     "\\[test\\]",      "brackets escaped")
assert_eq(util.escape_snippet("a\n[b]\nc"),  "a\\n\\[b\\]\\nc", "newlines + brackets")
assert_eq(util.escape_snippet("]]"),         "\\]\\]",          "multiple closing brackets")
assert_eq(util.escape_snippet("[[]]"),       "\\[\\[\\]\\]",    "nested brackets")
assert_eq(util.escape_snippet("no[special]here"), "no\\[special\\]here", "brackets mid-text")
assert_eq(util.escape_snippet("line1\nline2\nline3"), "line1\\nline2\\nline3", "multiple lines")

-- Tab escaping
assert_eq(util.escape_snippet("hello\tworld"),   "hello\\tworld",   "tab escaped")
assert_eq(util.escape_snippet("\t"),             "\\t",             "single tab")
assert_eq(util.escape_snippet("a\tb\tc"),        "a\\tb\\tc",       "multiple tabs")
assert_eq(util.escape_snippet("a\n\tb"),         "a\\n\\tb",        "newline + tab")
assert_eq(util.escape_snippet("\t\n"),           "\\t\\n",          "tab then newline")

-- Backtick escaping (needed for `name` plain+backtick format)
assert_eq(util.escape_snippet("`code`"),       "\\`code\\`",      "backticks escaped")
assert_eq(util.escape_snippet("a`b"),          "a\\`b",           "single backtick")
assert_eq(util.escape_snippet("``double``"),   "\\`\\`double\\`\\`", "double backticks")
assert_eq(util.escape_snippet("`[test]`"),     "\\`\\[test\\]\\`", "backticks + brackets")

-- Markdown italic/bold escaping (snippet is now outside backticks)
assert_eq(util.escape_snippet("_italic_"),     "\\_italic\\_",    "underscores escaped")
assert_eq(util.escape_snippet("*bold*"),       "\\*bold\\*",      "asterisks escaped")
assert_eq(util.escape_snippet("_*both*_"),     "\\_\\*both\\*\\_", "underscores + asterisks")
assert_eq(util.escape_snippet("normal_text"),  "normal\\_text",   "underscore mid-word")
assert_eq(util.escape_snippet("path/to/file_*"), "path/to/file\\_\\*", "mixed path chars")

-- Ordering: escape_snippet should be applied BEFORE shorten_snippet so that
-- the truncation accounts for the actual display length (escape adds chars).
-- If a snippet has newlines, the raw length is shorter than the escaped length.
-- shorten_snippet(first_escape then shorten) vs (shorten then escape)
-- A snippet "a\nb" has 3 raw bytes but displays as 4 chars ("a\\nb").
-- With max_len=3, shorten-then-escape would display 4 chars (bug),
-- while escape-then-shorten would correctly display "...".
local raw_with_newline = "a\nb"  -- 3 bytes
local max = 3
-- Current approach (buggy): shorten first, then escape — produces "a\\nb" (4 chars)
local buggy = util.escape_snippet(util.shorten_snippet(raw_with_newline, max))
-- Correct approach: escape first, then shorten — produces "..." (3 chars)
local correct = util.shorten_snippet(util.escape_snippet(raw_with_newline), max)
assert_eq(#buggy, 4, "shorten-then-escape: 'a\\nb' is 4 chars (would exceed max_len=3)")
assert_eq(#correct, 3, "escape-then-shorten: '...' is 3 chars (respects max_len=3)")
-- The correct result should be shorter or equal to max_len
assert_eq(#correct <= max, true, "escape-then-shorten respects max_len")

-- Multiple newlines: "a\nb\nc" (5 bytes), escaped "a\\nb\\nc" (8 chars), max_len=5
-- Current: shorten("a\nb\nc", 5) unchanged (5<=5), escape → "a\\nb\\nc" (8 chars) — exceeds!
-- Fixed: escape → "a\\nb\\nc" (8 chars), shorten(..., 5) → "..." (3 chars)
local raw_multi_nl = "a\nb\nc"  -- 5 bytes
local buggy2 = util.escape_snippet(util.shorten_snippet(raw_multi_nl, 5))
local correct2 = util.shorten_snippet(util.escape_snippet(raw_multi_nl), 5)
assert_eq(#buggy2, 7, "shorten-then-escape: 7 chars exceeds max_len=5")
assert_eq(#correct2 <= 5, true, "escape-then-shorten: respects max_len=5")

-- ──────────────────────────────────────────
heading("config.shortcuts")

-- Reload requires a fresh module (we need to reset state)
local function reload_config()
  package.loaded["hakka.config"] = nil
  return require("hakka.config")
end

local cfg_a = reload_config()
cfg_a.setup({ shortcuts = {} })
local sc = cfg_a.get_shortcuts()
-- Default shortcuts should be present
assert_eq(sc["<CR>"],  "submit", "default: <CR> -> submit")
assert_eq(sc["<C-s>"], "submit", "default: <C-s> -> submit")
assert_eq(sc["q"],     "close",  "default: q -> close")
-- All four defaults should be present
local count = 0
for _, _ in pairs(sc) do count = count + 1 end
assert_eq(count, 4, "default: 4 shortcuts")

-- User-defined shortcuts override defaults
local cfg_b = reload_config()
cfg_b.setup({ shortcuts = { ["<C-r>"] = "submit", ["<C-e>"] = "close" } })
sc = cfg_b.get_shortcuts()
assert_eq(sc["<CR>"],  "submit", "override: <CR> still submit")
assert_eq(sc["<C-s>"], "submit", "override: <C-s> still submit")
assert_eq(sc["q"],     "close",  "override: q still close")
assert_eq(sc["<C-r>"], "submit", "override: <C-r> -> submit (user added)")
assert_eq(sc["<C-e>"], "close",  "override: <C-e> -> close (user added)")
-- Should have 6 total (4 default + 2 user)
local count2 = 0
for _, _ in pairs(sc) do count2 = count2 + 1 end
assert_eq(count2, 6, "override: 6 shortcuts total")

-- is_user_shortcut / is_default_shortcut
assert_eq(cfg_b.is_default_shortcut("<CR>"),  true,  "<CR> is default")
assert_eq(cfg_b.is_default_shortcut("<C-s>"), true,  "<C-s> is default")
assert_eq(cfg_b.is_default_shortcut("q"),     true,  "q is default")
assert_eq(cfg_b.is_default_shortcut("<C-r>"), false, "<C-r> is not default")
assert_eq(cfg_b.is_user_shortcut("<C-r>"),    true,  "<C-r> is user")
assert_eq(cfg_b.is_user_shortcut("<C-e>"),    true,  "<C-e> is user")
assert_eq(cfg_b.is_user_shortcut("<CR>"),     false, "<CR> is not user (not overridden)")

-- Override an existing default
local cfg_c = reload_config()
cfg_c.setup({ shortcuts = { ["<CR>"] = "close" } })
sc = cfg_c.get_shortcuts()
assert_eq(sc["<CR>"], "close", "override: <CR> changed to close")
assert_eq(cfg_c.is_user_shortcut("<CR>"), true,  "<CR> is now user-overridden")
assert_eq(cfg_c.is_default_shortcut("<CR>"), false, "<CR> is no longer default")

-- Empty user shortcuts
local cfg_d = reload_config()
cfg_d.setup({})
sc = cfg_d.get_shortcuts()
assert_eq(sc["<CR>"], "submit", "empty opts: defaults work")

-- ──────────────────────────────────────────
heading("client.stream error frame completion (WebSocket)")

local bit = require("bit")
local bxor = bit.bxor
local bor = bit.bor

--- Build an unmasked WebSocket text frame (server → client).
--- @param payload string The text payload
--- @return string The raw frame bytes
local function ws_text_frame(payload)
  local len = #payload
  local header = string.char(0x81)  -- FIN=1, opcode=1 (text)
  if len < 126 then
    header = header .. string.char(len)  -- no mask bit
  elseif len < 65536 then
    header = header .. string.char(126, bit.rshift(len, 8), bit.band(len, 0xFF))
  else
    header = header .. string.char(127)
    for i = 7, 0, -1 do
      header = header .. string.char(bit.band(bit.rshift(len, i * 8), 0xFF))
    end
  end
  return header .. payload
end

local client = require("hakka.client")
local uv = vim.uv or vim.loop

local server = uv.new_tcp()
assert(server:bind("127.0.0.1", 0))
local sock = server:getsockname()
local ws_url = "ws://" .. sock.ip .. ":" .. tostring(sock.port) .. "/ws"
local server_client
local server_saw_request = false

server:listen(1, function(err)
  assert(not err, err)
  server_client = uv.new_tcp()
  server:accept(server_client)
  server_client:read_start(function(read_err, chunk)
    assert(not read_err, read_err)
    if not chunk then
      return
    end
    -- We received the HTTP upgrade request
    server_saw_request = true
    server_client:read_stop()

    -- Send HTTP 101 Switching Protocols response
    local http_resp = {
      "HTTP/1.1 101 Switching Protocols",
      "Upgrade: websocket",
      "Connection: Upgrade",
      "",
      "",
    }
    server_client:write(table.concat(http_resp, "\r\n"), function()
      -- Now send the WebSocket text frame with the error response
      local err_json = '{"session_id":"sid-err","error":"llm timeout"}'
      local ws_frame = ws_text_frame(err_json)
      server_client:write(ws_frame, function()
        -- Send a close frame
        local close_frame = string.char(0x88, 0x00)  -- FIN=1, opcode=8 (close), no payload
        server_client:write(close_frame, function()
          server_client:close()
          server:close()
        end)
      end)
    end)
  end)
end)

local saw_error_frame = false
local done_called = false
local done_err = "not-called"

client.stream(ws_url, { input = "timeout please" }, function(frame)
  if frame.error == "llm timeout" then
    saw_error_frame = true
  end
end, function(err)
  done_called = true
  done_err = err
end)

local completed = vim.wait(2000, function()
  return done_called
end, 10)

assert_eq(completed, true, "stream error frame calls on_done")
assert_eq(server_saw_request, true, "fake server received WebSocket upgrade request")
assert_eq(saw_error_frame, true, "stream forwards error frame before done")
assert_eq(done_err, nil, "error frame closes stream without transport error")

-- ──────────────────────────────────────────
heading("ui.prompts")

-- Clean up any lingering hakka buffers from previous tests.
for _, b in ipairs(vim.api.nvim_list_bufs()) do
  local lines = vim.api.nvim_buf_get_lines(b, 0, 2, false)
  if #lines >= 1 and lines[1] == "# Me" then
    pcall(vim.api.nvim_buf_delete, b, { force = true })
  end
end

-- Helper: get a fresh, isolated UI module (clear its internal state)
local function fresh_ui()
  package.loaded["hakka.ui"] = nil
  return require("hakka.ui")
end

-- Test that M.open creates a buffer with the new user prompt (# Me)
do
  local ui = fresh_ui()
  -- open() will try to create a window; in headless mode that may fail,
  -- but the buffer should still be created with the correct prompt text.
  local ok_open = pcall(ui.open, function() end)

  -- Find the newly created buffer by scanning all buffers for our header
  local found = false
  local buf_lines = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, 2, false)
    if #lines >= 1 and lines[1] == "# Me" then
      found = true
      buf_lines = lines
      break
    end
  end

  if found then
    assert_eq(true, true, "M.open creates buffer with '# Me' prompt")
    assert_eq(buf_lines[1], "# Me", "first line is '# Me'")
    -- line 2 should be empty (the blank input line)
    assert_eq(buf_lines[2], "", "second line is blank input line")
  else
    -- If no buffer was found, the test failed because the old prompt is still there
    -- Try to find old-style prompt to confirm it would have been found if present
    for _, b in ipairs(vim.api.nvim_list_bufs()) do
      local lines = vim.api.nvim_buf_get_lines(b, 0, 2, false)
      if #lines >= 1 and lines[1] == "### you" then
        assert_eq(lines[1], "# Me",
          "buffer still has old prompt '### you' instead of '# Me'")
        found = true
        break
      end
    end
    if not found then
      -- Neither new nor old found – report failure
      assert_eq(true, false,
        "could not find any buffer with '# Me' (or old '### you') prompt")
    end
  end
end

-- Test M.clear() resets the buffer to "# Me" prompt
do
  local ui = fresh_ui()
  -- Create a buffer and inject a non-standard state so we can test clear()
  local test_buf = vim.api.nvim_create_buf(false, true)
  vim.api.nvim_buf_set_lines(test_buf, 0, -1, false, {
    "# Hakka", "some assistant text", ""
  })

  -- We need to access state.buf; since it's module-local, we call open()
  -- first to initialise, then close the failed window.
  -- Instead, directly test by calling clear() after open()
  local ok_open = pcall(ui.open, function() end)
  -- Now clear should reset to "# Me"
  ui.clear()

  -- Check the buffer content
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, 2, false)
    if #lines >= 1 and (lines[1] == "# Me" or lines[1] == "### you") then
      assert_eq(lines[1], "# Me",
        "after clear() buffer has '# Me' instead of old '### you'")
      break
    end
  end
end

-- ──────────────────────────────────────────
heading("ui.load_history")

do
  local ui = fresh_ui()
  -- Open to create the buffer, then close so state.win=nil and width
  pcall(ui.open, function() end)

  -- Sample messages to load as history
  local messages = {
    { role = "user", content = "hello there" },
    { role = "assistant", content = "hi, how can I help?" },
    -- assistant message with only tool calls (empty content) — should be skipped
    { role = "assistant", content = "" },
    { role = "user", content = "tell me a joke" },
    { role = "assistant", content = "why did the chicken cross the road?\nto get to the other side!" },
  }

  ui.load_history(messages)

  -- Find the buffer that received the history
  local buf_lines = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
    local has_hello = false
    for _, l in ipairs(lines) do
      if l:find("hello there") then
        has_hello = true
        break
      end
    end
    if has_hello then
      buf_lines = lines
      break
    end
  end

  assert_eq(#buf_lines > 0, true, "buffer has content after load_history")

  -- All messages should be rendered
  local found_hello   = false
  local found_help    = false
  local found_joke_q  = false
  local found_joke_a  = false
  for _, l in ipairs(buf_lines) do
    if l == "hello there"                          then found_hello  = true end
    if l == "hi, how can I help?"                  then found_help   = true end
    if l == "tell me a joke"                       then found_joke_q = true end
    if l:find("why did the chicken")               then found_joke_a = true end
  end
  assert_eq(found_hello,  true, "first user message present")
  assert_eq(found_help,   true, "first assistant reply present")
  assert_eq(found_joke_q, true, "second user message present")
  assert_eq(found_joke_a, true, "second assistant reply present")

  -- Buffer should have a user prompt for new input
  local has_prompt = false
  for _, l in ipairs(buf_lines) do
    if l == "# Me" then has_prompt = true end
  end
  assert_eq(has_prompt, true, "prompt '# Me' present after load_history")

  -- Ensure no empty # Hakka sections (from empty-content messages like tool calls)
  local hakka_count = 0
  for _, l in ipairs(buf_lines) do
    if l == "# Hakka" then hakka_count = hakka_count + 1 end
  end
  assert_eq(hakka_count, 2, "exactly 2 '# Hakka' headers (empty assistant message skipped, consecutive same-role compressed)")

  -- # Me also appears at the end as the prompt, so we should have 3 total
  local me_count = 0
  for _, l in ipairs(buf_lines) do
    if l == "# Me" then me_count = me_count + 1 end
  end
  -- 2 user messages in history + 1 prompt = 3
  assert_eq(me_count, 3, "# Me count includes 2 from history + 1 prompt")
end

-- ──────────────────────────────────────────
heading("ui.load_history with consecutive same-role messages")

do
  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- Simulate real session pattern: user, assistant (with tool calls → empty content),
  -- then another assistant (with real content), user, assistant, assistant.
  local messages = {
    { role = "user", content = "discover project" },
    { role = "assistant", content = "Let me check the files." },
    -- assistant with only tool calls — empty content, should be skipped
    { role = "assistant", content = "" },
    -- another assistant turn (real content) — header should NOT repeat
    { role = "assistant", content = "Here are the results." },
    { role = "user", content = "implement feature" },
    { role = "assistant", content = "I will add the code." },
    -- another assistant after tool calls — content only, header compressed
    { role = "assistant", content = "Done and tested." },
  }

  ui.load_history(messages)

  local buf_lines = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
    local has_results = false
    for _, l in ipairs(lines) do
      if l == "Here are the results." then
        has_results = true
        break
      end
    end
    if has_results then
      buf_lines = lines
      break
    end
  end

  assert_eq(#buf_lines > 0, true, "buffer has content after same-role load_history")

  -- Count headers
  local hakka_count = 0
  local me_count = 0
  for _, l in ipairs(buf_lines) do
    if l == "# Hakka" then hakka_count = hakka_count + 1 end
    if l == "# Me" then me_count = me_count + 1 end
  end

  -- Messages: user(discover)[Me], assistant(check)[Hakka],
  --   assistant(empty)skip, assistant(results)[NO header - same role],
  --   user(implement)[Me], assistant(code)[Hakka],
  --   assistant(tested)[NO header - same role]
  -- So: Hakka=2, Me=2 (+1 prompt = 3)
  assert_eq(hakka_count, 2, "consecutive assistant messages share header (2 groups)")
  assert_eq(me_count, 3, "with prompt, 3 # Me total (2 in history + 1 prompt)")

  -- Verify order of content
  local idx_let_me    = nil
  local idx_results   = nil
  local idx_implement = nil
  local idx_code      = nil
  local idx_tested    = nil
  for i, l in ipairs(buf_lines) do
    if l == "Let me check the files."    then idx_let_me    = i end
    if l == "Here are the results."      then idx_results   = i end
    if l == "implement feature"          then idx_implement = i end
    if l == "I will add the code."       then idx_code      = i end
    if l == "Done and tested."           then idx_tested    = i end
  end
  -- results should come right after "let me check" (same group, no header between)
  assert_eq(idx_results ~= nil, true,  "results content present")
  assert_eq(idx_let_me  ~= nil, true,  "first assistant content present")
  assert_eq(idx_results > idx_let_me, true, "results after first content")
  -- implement should come after results (new user group)
  assert_eq(idx_implement ~= nil, true, "user 'implement' present")
  assert_eq(idx_implement > idx_results, true, "user after assistant")
  -- code then tested (same group, no header)
  assert_eq(idx_code  ~= nil, true, "code content present")
  assert_eq(idx_tested ~= nil, true, "tested content present")
  assert_eq(idx_tested > idx_code, true, "tested after code")
  -- no # Hakka between code and tested
  local hakka_between = false
  for i = idx_code, idx_tested do
    if buf_lines[i] == "# Hakka" then hakka_between = true end
  end
  assert_eq(hakka_between, false, "no # Hakka between consecutive assistant contents")
end

-- ──────────────────────────────────────────
heading("ui.load_history with trailing user message")

do
  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- History ending with a user message — prompt should NOT double the # Me.
  local messages = {
    { role = "user", content = "Hello" },
    { role = "assistant", content = "Hi there!" },
    { role = "user", content = "What's up?" },
  }

  ui.load_history(messages)

  local buf_lines = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
    -- Look for a buffer that has our trailing user content
    local has_whatsup = false
    for _, l in ipairs(lines) do
      if l == "What's up?" then has_whatsup = true; break end
    end
    if has_whatsup then
      buf_lines = lines
      break
    end
  end

  assert_eq(#buf_lines > 0, true, "found buffer with trailing user message")

  -- Count # Me headers
  local me_count = 0
  for _, l in ipairs(buf_lines) do
    if l == "# Me" then me_count = me_count + 1 end
  end
  -- 2 user messages in history, last_role="user" so no extra prompt header → 2
  assert_eq(me_count, 2, "trailing user: only 2 # Me (no duplicate prompt)")

  -- Last non-blank line should be "What's up?" (the last user content)
  local last_content = nil
  for i = #buf_lines, 1, -1 do
    if buf_lines[i] ~= "" then
      last_content = buf_lines[i]
      break
    end
  end
  assert_eq(last_content, "What's up?", "trailing user: last content is the user message")
end

-- ──────────────────────────────────────────
heading("ui.append_tool_event format — backtick name plus plain snippet")

do
  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- Start a tool call — should produce "`name` snippet" (no brackets)
  ui.append_tool_event("read_file", "start", "foo.txt")

  -- Find the hakka buffer
  local buf_lines = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
    for _, l in ipairs(lines) do
      if l:find("read_file") then
        buf_lines = lines
        break
      end
    end
    if #buf_lines > 0 then break end
  end

  assert_eq(#buf_lines > 0, true, "buffer has tool event line")

  local found_new = false
  for _, l in ipairs(buf_lines) do
    -- Format: `read_file` foo.txt (backtick name, then plain snippet)
    if l:find("^`read_file` foo%.txt$") then
      found_new = true
    end
  end
  assert_eq(found_new, true, "tool start line uses `name` snippet format")

  -- Complete the tool call
  ui.append_tool_event("read_file", "ok", "foo.txt")

  -- Re-read buffer (fresh scan)
  buf_lines = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
    for _, l in ipairs(lines) do
      if l:find("read_file") then
        buf_lines = lines
        break
      end
    end
    if #buf_lines > 0 then break end
  end

  local found_ok = false
  for _, l in ipairs(buf_lines) do
    -- Same format: `read_file` foo.txt — no symbol
    if l:find("^`read_file` foo%.txt$") then
      found_ok = true
    end
  end
  assert_eq(found_ok, true, "tool completed line still uses `name` snippet format (no symbol)")
end

-- ──────────────────────────────────────────
heading("ui.append_tool_event truncation — cap increased to 100")

do
  local ui = fresh_ui()
  -- Open to create the buffer, then close so state.win=nil and width
  -- falls back to vim.o.columns (80 in headless).
  pcall(ui.open, function() end)
  -- Close window so state.win=nil → width falls back to vim.o.columns
  ui.close()

  -- In headless mode, vim.o.columns = 80. max_line = min(100, 80) = 80.
  -- Overhead = backtick(1) + backtick(1) + space(1) = 3 chars.
  -- snippet_max = max(10, 80 - 2 - 3) = 75.
  -- Verify that a 75-char snippet fits.
  local tname = "rd"
  local snippet_75 = string.rep("x", 75)
  ui.append_tool_event(tname, "start", snippet_75)

  local buf_lines = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
    for _, l in ipairs(lines) do
      if l:find(tname) then
        buf_lines = lines
        break
      end
    end
    if #buf_lines > 0 then break end
  end

  assert_eq(#buf_lines > 0, true, "buffer has tool event line")

  local found = false
  for _, l in ipairs(buf_lines) do
    -- 75 x's should fit exactly (overhead = 3: backtick + backtick + space)
    if l:find("^`" .. tname .. "` " .. string.rep("x", 75) .. "$") then
      found = true
    end
  end
  assert_eq(found, true,
    "75-char snippet fits (overhead 3, no symbol)")
end

-- ──────────────────────────────────────────
heading("ui.append_tool_event fallback — no matching pending")

do
  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- Complete a tool without starting it first — should fallback to append
  local tname = "zz_fallback"
  ui.append_tool_event(tname, "ok", "no-start")

  local buf_lines = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
    for _, l in ipairs(lines) do
      if l:find(tname) then
        buf_lines = lines
        break
      end
    end
    if #buf_lines > 0 then break end
  end

  assert_eq(#buf_lines > 0, true, "buffer has fallback tool line")

  local found = false
  for _, l in ipairs(buf_lines) do
    -- Fallback format: `zz_fallback` no-start (backtick name, then plain snippet)
    if l:find("^`" .. tname .. "` no%-start$") then
      found = true
    end
  end
  assert_eq(found, true, "fallback tool line uses `name` snippet format (no symbol)")
end

-- ──────────────────────────────────────────
heading("config.cancel shortcut")

-- Verify that cancel shortcut is present in defaults
local cfg_e = reload_config()
cfg_e.setup({})
sc = cfg_e.get_shortcuts()
assert_eq(sc["<C-c>"], "cancel", "default: <C-c> -> cancel")
-- Now 4 defaults: <CR>=submit, <C-s>=submit, q=close, <C-c>=cancel
local count3 = 0
for _, _ in pairs(sc) do count3 = count3 + 1 end
assert_eq(count3, 4, "default: 4 shortcuts (submit x2, close, cancel)")

-- User can override cancel shortcut
local cfg_f = reload_config()
cfg_f.setup({ shortcuts = { ["<C-x>"] = "cancel" } })
sc = cfg_f.get_shortcuts()
assert_eq(sc["<C-c>"], "cancel", "override: <C-c> still cancel (default)")
assert_eq(sc["<C-x>"], "cancel", "override: <C-x> -> cancel (user added)")
assert_eq(cfg_f.is_user_shortcut("<C-x>"), true, "<C-x> is user shortcut")
assert_eq(cfg_f.is_default_shortcut("<C-c>"), true, "<C-c> still default")

-- ──────────────────────────────────────────
heading("ui.prompt_start — no accumulated # Me after multiple turns")

-- Simulate the conversation cycle: user sends → assistant responds → prompt appears.
-- Verify that old # Me headers don't accumulate.
do
  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- Helper to get lines from our test buffer (identified by unique content marker)
  local function get_test_lines()
    for _, b in ipairs(vim.api.nvim_list_bufs()) do
      local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
      for _, l in ipairs(lines) do
        if l == "MARKER_TURN1" or l == "MARKER_TURN2" or l == "MARKER_TURN3" then
          return lines
        end
      end
    end
    return {}
  end

  local function count_me_in(lines)
    local count = 0
    for _, l in ipairs(lines) do
      if l == "# Me" then count = count + 1 end
    end
    return count
  end

  -- Turn 1: user says "MARKER_TURN1", assistant responds "turn1 response"
  ui.append_user("MARKER_TURN1")
  ui.begin_assistant_stream()
  ui.append_delta("turn1 response")
  ui.end_assistant_stream()

  local lines1 = get_test_lines()
  local count1 = count_me_in(lines1)
  -- After turn 1: 1 # Me label for MARKER_TURN1 + 1 # Me prompt = 2
  assert_eq(count1, 2,
    "after turn 1: exactly 2 '# Me' (1 label + 1 prompt), got " .. count1 ..
    ", lines: " .. vim.inspect(lines1))

  -- Turn 2
  ui.append_user("MARKER_TURN2")
  ui.begin_assistant_stream()
  ui.append_delta("turn2 response")
  ui.end_assistant_stream()

  local lines2 = get_test_lines()
  local count2 = count_me_in(lines2)
  -- After turn 2: 2 labels + 1 prompt = 3
  assert_eq(count2, 3,
    "after turn 2: exactly 3 '# Me' (2 labels + 1 prompt), got " .. count2 ..
    ", lines: " .. vim.inspect(lines2))

  -- Turn 3
  ui.append_user("MARKER_TURN3")
  ui.begin_assistant_stream()
  ui.append_delta("turn3 response")
  ui.end_assistant_stream()

  local lines3 = get_test_lines()
  local count3 = count_me_in(lines3)
  -- After turn 3: 3 labels + 1 prompt = 4
  assert_eq(count3, 4,
    "after turn 3: exactly 4 '# Me' (3 labels + 1 prompt), got " .. count3 ..
    ", lines: " .. vim.inspect(lines3))
end

-- ──────────────────────────────────────────
heading("ui.append_tool_event after close — no crash")

-- When the user closes the chat window while a tool call is in flight,
-- the completion event should not crash (buffer may be invalid).
do
  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- Start a tool call
  ui.append_tool_event("read_file", "start", "some-file.txt")

  -- Simulate buffer deletion (e.g. user explicitly wipes the buffer)
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
    for _, l in ipairs(lines) do
      if l:find("`read_file` some%-file%.txt") then
        vim.api.nvim_buf_delete(b, { force = true })
        break
      end
    end
  end

  -- Tool completion event arrives after buffer deleted — should not error
  local ok, err = pcall(ui.append_tool_event, "read_file", "ok", "some-file.txt")
  assert_eq(ok, true, "tool ok after delete: no crash, got err=" .. tostring(err))
end

-- ──────────────────────────────────────────
heading("init.cancel")

-- Test the M.cancel function in isolation
local init = require("hakka.init")

-- Verify cancel function exists
assert_eq(type(init.cancel), "function", "M.cancel is a function")

-- ──────────────────────────────────────────
heading("ui.append_user preserves # Me header")

-- Verify that append_user does NOT remove the # Me label from the buffer.
-- Bug: a misguided fix for double-# Me removed the existing # Me header,
-- causing the first user message to appear without a label.
do
  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- Helper: find our test buffer by searching for marker text
  local function get_buf_lines()
    for _, b in ipairs(vim.api.nvim_list_bufs()) do
      local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
      for _, l in ipairs(lines) do
        if l == "hello world" then
          return lines
        end
      end
    end
    return {}
  end

  -- User types and submits "hello world"
  ui.append_user("hello world")

  local lines = get_buf_lines()
  assert_eq(#lines >= 1, true, "buffer found with user text")
  -- The # Me header should still be there as the user message label
  assert_eq(lines[1], "# Me", "append_user preserves '# Me' header")
  -- The user text should be after the # Me
  local found_user_text = false
  for _, l in ipairs(lines) do
    if l == "hello world" then found_user_text = true end
  end
  assert_eq(found_user_text, true, "user text present in buffer after append_user")
end

-- ──────────────────────────────────────────
heading("ui.append_assistant not stream-safe — documents the bug")

-- append_assistant() is a convenience function for non-stream output: it
-- prepends "# Hakka" and calls reset_prompt (# Me). Calling it during an
-- active stream (as handle_command_result does) causes a second # Hakka
-- header. The # Me duplication was fixed by making reset_prompt idempotent.
-- Use append_delta for stream output.
do
  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- First turn: normal user → assistant cycle (marker ensures unique buffer search)
  ui.append_user("first msg turn2")
  ui.begin_assistant_stream()
  ui.append_delta("first response turn2")
  ui.end_assistant_stream()

  -- Find our test buffer by searching for the turn2 marker
  local function get_test_lines()
    for _, b in ipairs(vim.api.nvim_list_bufs()) do
      local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
      for _, l in ipairs(lines) do
        if l == "first msg turn2" then
          return lines
        end
      end
    end
    return {}
  end

  -- Now simulate a JSON command: begin stream → call append_assistant
  -- (buggy pattern from handle_command_result) → end stream
  ui.append_user("/session list")
  ui.begin_assistant_stream()
  ui.append_assistant("list output here")  -- BUG: not stream-safe (adds 2nd # Hakka)
  ui.end_assistant_stream()

  local lines = get_test_lines()

  -- Count headers
  local me_count = 0
  local hakka_count = 0
  for _, l in ipairs(lines) do
    if l == "# Me" then me_count = me_count + 1 end
    if l == "# Hakka" then hakka_count = hakka_count + 1 end
  end

  -- append_assistant during stream still adds a SECOND # Hakka header
  -- (one from begin_assistant_stream, one from append_assistant)
  assert_eq(hakka_count >= 2, true,
    "append_assistant during stream causes >=2 # Hakka (duplicate header)")
  -- # Me prompt is no longer duplicated (reset_prompt is idempotent).
  -- After 2 turns: 2 labels + 1 prompt = 3
  assert_eq(me_count, 3,
    "append_assistant during stream: 3 # Me (2 labels + 1 prompt), got " .. me_count)
end

-- ──────────────────────────────────────────
heading("plugin.HakkaCancel command")

-- Source the plugin file; first clear the loaded guard so it registers commands
vim.g.loaded_hakka = nil
dofile(root .. "plugin/hakka.lua")

-- Verify the :HakkaCancel command exists
local has_cancel_cmd = false
for name, _ in pairs(vim.api.nvim_get_commands({})) do
  if name == "HakkaCancel" then
    has_cancel_cmd = true
    break
  end
end
assert_eq(has_cancel_cmd, true, ":HakkaCancel command exists")

-- ──────────────────────────────────────────


-- ──────────────────────────────────────────
heading("ui.append_tool_event result NOT displayed — name and snippet only")

-- When data.result is present, the tool completion line should show only
-- `name` snippet, NOT `name` snippet → result. The result is visible in
-- the LLM's response text, which the assistant streams separately.
do
  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- Start a tool call
  ui.append_tool_event("grep", "start", "README.md")

  -- Complete with data.result present (this is what the server sends)
  ui.append_tool_event("grep", "ok", "README.md", { result = "found 42 matches" })

  -- Find the buffer and inspect the line
  local buf_lines = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
    for _, l in ipairs(lines) do
      if l:find("grep") then
        buf_lines = lines
        break
      end
    end
    if #buf_lines > 0 then break end
  end

  assert_eq(#buf_lines > 0, true, "buffer has tool event line for grep")

  -- The line should NOT contain the result
  local has_result = false
  local only_name_snippet = false
  for _, l in ipairs(buf_lines) do
    -- Expected: `grep(README.md)` WITHOUT `→ found 42 matches`
    if l:find("^`grep` README%.md$") then
      only_name_snippet = true
    end
    -- This would be the OLD buggy format with result appended
    if l:find("→") or l:find("found 42 matches") then
      has_result = true
    end
  end

  assert_eq(has_result, false, "tool completion line does NOT contain result data")
  assert_eq(only_name_snippet, true, "tool completion line shows only `name` snippet")
end

-- ──────────────────────────────────────────
heading("ui.append_tool_event err result NOT displayed — name and snippet only")

-- Same for error status: no result should be shown.
do
  local ui = fresh_ui()
  pcall(ui.open, function() end)

  -- Start a tool call
  ui.append_tool_event("http_get", "start", "https://example.com")

  -- Complete with error result
  ui.append_tool_event("http_get", "err", "https://example.com", { result = "Error: connection refused" })

  local buf_lines = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local lines = vim.api.nvim_buf_get_lines(b, 0, -1, false)
    for _, l in ipairs(lines) do
      if l:find("http_get") then
        buf_lines = lines
        break
      end
    end
    if #buf_lines > 0 then break end
  end

  assert_eq(#buf_lines > 0, true, "buffer has tool event line for http_get")

  local has_result = false
  local only_name_snippet = false
  for _, l in ipairs(buf_lines) do
    if l:find("^`http_get` https://example%.com$") then
      only_name_snippet = true
    end
    if l:find("connection refused") then
      has_result = true
    end
  end

  assert_eq(has_result, false, "err tool line does NOT contain result data")
  assert_eq(only_name_snippet, true, "err tool line shows only `name` snippet")
end
if not ok then
  io.stderr:write("SOME TESTS FAILED\n")
  vim.cmd("cquit")  -- exit with non-zero status
else
  vim.cmd("qall!")  -- exit cleanly
end

