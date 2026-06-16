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
-- All three defaults should be present
local count = 0
for _, _ in pairs(sc) do count = count + 1 end
assert_eq(count, 3, "default: 3 shortcuts")

-- User-defined shortcuts override defaults
local cfg_b = reload_config()
cfg_b.setup({ shortcuts = { ["<C-r>"] = "submit", ["<C-e>"] = "close" } })
sc = cfg_b.get_shortcuts()
assert_eq(sc["<CR>"],  "submit", "override: <CR> still submit")
assert_eq(sc["<C-s>"], "submit", "override: <C-s> still submit")
assert_eq(sc["q"],     "close",  "override: q still close")
assert_eq(sc["<C-r>"], "submit", "override: <C-r> -> submit (user added)")
assert_eq(sc["<C-e>"], "close",  "override: <C-e> -> close (user added)")
-- Should have 5 total (3 default + 2 user)
local count2 = 0
for _, _ in pairs(sc) do count2 = count2 + 1 end
assert_eq(count2, 5, "override: 5 shortcuts total")

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
heading("client.stream error frame completion")

local client = require("hakka.client")
local uv = vim.uv or vim.loop

local server = uv.new_tcp()
assert(server:bind("127.0.0.1", 0))
local sock = server:getsockname()
local addr = sock.ip .. ":" .. tostring(sock.port)
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
    server_saw_request = true
    server_client:read_stop()
    server_client:write('{"session_id":"sid-err","error":"llm timeout"}\n', function()
      server_client:close()
      server:close()
    end)
  end)
end)

local saw_error_frame = false
local done_called = false
local done_err = "not-called"

client.stream(addr, { input = "timeout please" }, function(frame)
  if frame.error == "llm timeout" then
    saw_error_frame = true
  end
end, function(err)
  done_called = true
  done_err = err
end)

local completed = vim.wait(1000, function()
  return done_called
end, 10)

assert_eq(completed, true, "stream error frame calls on_done")
assert_eq(server_saw_request, true, "fake server received stream request")
assert_eq(saw_error_frame, true, "stream forwards error frame before done")
assert_eq(done_err, nil, "error frame closes stream without transport error")

-- ──────────────────────────────────────────
heading("ui.prompts")

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

if not ok then
  io.stderr:write("SOME TESTS FAILED\n")
  vim.cmd("cquit")  -- exit with non-zero status
else
  vim.cmd("qall!")  -- exit cleanly
end