---
-- Tests for parse_slash_command in init.lua
--
-- Since parse_slash_command is a local function, we replicate its logic.
-- Usage: nvim --headless -c "lua dofile('tests/slash_commands_test.lua')" -c "qa!"

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

-- Replicate the exact parse_slash_command logic from init.lua
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
  -- This handles the common pattern of typing slash commands without a space.
  local known_prefixes = { "/session", "/model", "/tool", "/models" }
  for _, prefix in ipairs(known_prefixes) do
    if cmd:match("^" .. prefix .. "_") then
      local suffix = cmd:sub(#prefix + 2) -- after prefix and underscore
      -- Rebuild parts: prefix, suffix, then any remaining original arguments
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

  -- /continue — JSON command, server continues the LLM turn
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
      return { cmd = "session_info", params = {} }
    elseif sub == "switch" or sub == "get" then
      local name = parts[3]
      if name then
        return { cmd = "model_switch", params = { name = name } }
      end
    end
    -- /model alone or with unknown sub
    if sub and sub ~= "list" and sub ~= "show" and sub ~= "get" then
      -- treat as /model <name> (short form)
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

-- Helper to compare two tables
local function table_eq(a, b)
  if type(a) ~= type(b) then return false end
  if type(a) ~= "table" then return a == b end
  local a_keys = {}
  for k in pairs(a) do a_keys[k] = true end
  for k in pairs(b) do 
    if not a_keys[k] then return false end
    a_keys[k] = nil
  end
  for k in pairs(a_keys) do return false end -- extra keys in a
  for k, v in pairs(a) do
    if not table_eq(v, b[k]) then return false end
  end
  return true
end

local function assert_table_eq(got, expected, msg)
  total = total + 1
  if table_eq(got, expected) then
    passed = passed + 1
    return
  end
  ok = false
  failed = failed + 1
  local prefix = msg or ""
  if prefix ~= "" then prefix = prefix .. ": " end
  io.stderr:write(string.format("  FAIL  %sgot %s, expected %s\n", prefix, vim.inspect(got), vim.inspect(expected)))
end

-- ──────────────────────────────────────────
io.write("--- parse_slash_command ---\n")

-- Basic slash commands
local r = parse_slash_command("/help")
assert_table_eq(r, { cmd = "help", params = {} }, "/help")

r = parse_slash_command("/continue")
assert_table_eq(r, { cmd = "continue", params = {} }, "/continue")

r = parse_slash_command("/start")
assert_table_eq(r, { cmd = "start", params = {} }, "/start")

r = parse_slash_command("/models")
assert_table_eq(r, { cmd = "model_list", params = {} }, "/models")

-- /session subcommands
r = parse_slash_command("/session list")
assert_table_eq(r, { cmd = "session_list", params = {} }, "/session list")

r = parse_slash_command("/session create")
assert_table_eq(r, { cmd = "session_create", params = {} }, "/session create")

r = parse_slash_command("/session info")
assert_table_eq(r, { cmd = "session_info", params = {} }, "/session info")

r = parse_slash_command("/session autorename")
assert_table_eq(r, { cmd = "session_autorename", params = {} }, "/session autorename")

r = parse_slash_command("/session delete abc123")
assert_table_eq(r, { cmd = "session_delete", params = { id = "abc123" } }, "/session delete abc123")

r = parse_slash_command("/session get abc123")
assert_table_eq(r, { cmd = "get_session", params = { id = "abc123" } }, "/session get abc123")

r = parse_slash_command("/session switch abc123")
assert_table_eq(r, { cmd = "get_session", params = { id = "abc123" } }, "/session switch abc123 (alias for get)")

r = parse_slash_command("/session switch")
assert_table_eq(r, { cmd = "get_session", params = { id = "" } }, "/session switch without id")

r = parse_slash_command("/session rename My Chat Room")
assert_table_eq(r, { cmd = "session_rename", params = { name = "My Chat Room" } }, "/session rename My Chat Room")

-- /session without subcommand should return nil (not sent as command)
r = parse_slash_command("/session")
assert_eq(r, nil, "/session alone returns nil")

-- /session get without id
r = parse_slash_command("/session get")
assert_table_eq(r, { cmd = "get_session", params = { id = "" } }, "/session get without id")

-- /session delete without id
r = parse_slash_command("/session delete")
assert_table_eq(r, { cmd = "session_delete", params = { id = "" } }, "/session delete without id")

-- /model subcommands
r = parse_slash_command("/model list")
assert_table_eq(r, { cmd = "model_list", params = {} }, "/model list")

r = parse_slash_command("/model show")
assert_table_eq(r, { cmd = "session_info", params = {} }, "/model show")

r = parse_slash_command("/model gpt4")
assert_table_eq(r, { cmd = "model_switch", params = { name = "gpt4" } }, "/model gpt4 (short form)")

-- /model alone returns session_info
r = parse_slash_command("/model")
assert_table_eq(r, { cmd = "session_info", params = {} }, "/model alone -> session_info")

-- /tool subcommands
r = parse_slash_command("/tool list")
assert_table_eq(r, { cmd = "tool_list", params = {} }, "/tool list")

-- /tool enable maps to tool_allow (fixed: was tool_enable)
r = parse_slash_command("/tool enable read_file")
assert_table_eq(r, { cmd = "tool_allow", params = { name = "read_file" } }, "/tool enable read_file -> tool_allow")

-- /tool disable maps to tool_deny (fixed: was tool_disable)
r = parse_slash_command("/tool disable write_file")
assert_table_eq(r, { cmd = "tool_deny", params = { name = "write_file" } }, "/tool disable write_file -> tool_deny")

-- /tool allow and /tool deny also work (new aliases)
r = parse_slash_command("/tool allow read_file")
assert_table_eq(r, { cmd = "tool_allow", params = { name = "read_file" } }, "/tool allow read_file -> tool_allow")

r = parse_slash_command("/tool deny write_file")
assert_table_eq(r, { cmd = "tool_deny", params = { name = "write_file" } }, "/tool deny write_file -> tool_deny")

r = parse_slash_command("/tool enable")
assert_eq(r, nil, "/tool enable without name returns nil")

r = parse_slash_command("/tool disable")
assert_eq(r, nil, "/tool disable without name returns nil")

-- Non-slash text returns nil
r = parse_slash_command("hello world")
assert_eq(r, nil, "plain text returns nil")

r = parse_slash_command("")
assert_eq(r, nil, "empty string returns nil")

-- Whitespace-only
r = parse_slash_command("  ")
assert_eq(r, nil, "whitespace returns nil")

-- @bot_username suffix stripping (command only, subcommands not stripped)
r = parse_slash_command("/help@MyBot")
assert_table_eq(r, { cmd = "help", params = {} }, "/help@MyBot strips suffix")

-- Note: @suffix on subcommands is not stripped; not relevant for Neovim
r = parse_slash_command("/session list@MyBot")
assert_eq(r, nil, "/session list@MyBot: @ suffix not stripped from subcommand")

-- /cwd_set
r = parse_slash_command("/cwd_set /path/to/dir")
assert_table_eq(r, { cmd = "cwd_set", params = { cwd = "/path/to/dir" } }, "/cwd_set /path/to/dir")

r = parse_slash_command("/cwd_set")
assert_eq(r, nil, "/cwd_set without path returns nil")

-- /compact
r = parse_slash_command("/compact")
assert_table_eq(r, { cmd = "compact", params = {} }, "/compact without n")

r = parse_slash_command("/compact 50000")
assert_table_eq(r, { cmd = "compact", params = { n = 50000 } }, "/compact 50000")

-- Unknown commands return nil
r = parse_slash_command("/unknown")
assert_eq(r, nil, "/unknown returns nil")

r = parse_slash_command("/foobar")
assert_eq(r, nil, "/foobar returns nil")

-- Subcommand that doesn't exist
r = parse_slash_command("/session foo")
assert_eq(r, nil, "/session foo returns nil (unknown subcommand)")

r = parse_slash_command("/tool foo")
assert_eq(r, nil, "/tool foo returns nil (unknown subcommand)")

-- Combined forms (underscore instead of space) — must be normalized
r = parse_slash_command("/session_list")
assert_table_eq(r, { cmd = "session_list", params = {} }, "/session_list (combined)")

r = parse_slash_command("/session_create")
assert_table_eq(r, { cmd = "session_create", params = {} }, "/session_create (combined)")

r = parse_slash_command("/session_info")
assert_table_eq(r, { cmd = "session_info", params = {} }, "/session_info (combined)")

r = parse_slash_command("/session_autorename")
assert_table_eq(r, { cmd = "session_autorename", params = {} }, "/session_autorename (combined)")

r = parse_slash_command("/session_delete abc123")
assert_table_eq(r, { cmd = "session_delete", params = { id = "abc123" } }, "/session_delete abc123 (combined)")

r = parse_slash_command("/session_get abc123")
assert_table_eq(r, { cmd = "get_session", params = { id = "abc123" } }, "/session_get abc123 (combined)")

r = parse_slash_command("/session_switch abc123")
assert_table_eq(r, { cmd = "get_session", params = { id = "abc123" } }, "/session_switch abc123 (combined)")

r = parse_slash_command("/session_rename My Chat")
assert_table_eq(r, { cmd = "session_rename", params = { name = "My Chat" } }, "/session_rename My Chat (combined)")

r = parse_slash_command("/model_list")
assert_table_eq(r, { cmd = "model_list", params = {} }, "/model_list (combined)")

r = parse_slash_command("/model_switch gpt4")
assert_table_eq(r, { cmd = "model_switch", params = { name = "gpt4" } }, "/model_switch gpt4 (combined)")

-- /model switch <name> (two-word form)
r = parse_slash_command("/model switch gpt4")
assert_table_eq(r, { cmd = "model_switch", params = { name = "gpt4" } }, "/model switch gpt4 (two-word form)")

r = parse_slash_command("/tool_list")
assert_table_eq(r, { cmd = "tool_list", params = {} }, "/tool_list (combined)")

r = parse_slash_command("/tool_allow read_file")
assert_table_eq(r, { cmd = "tool_allow", params = { name = "read_file" } }, "/tool_allow read_file (combined)")

r = parse_slash_command("/tool_deny write_file")
assert_table_eq(r, { cmd = "tool_deny", params = { name = "write_file" } }, "/tool_deny write_file (combined)")

-- ──────────────────────────────────────────
io.write("\n--- Summary ---\n")

if not ok then
  io.stderr:write(string.format("FAILED: %d passed, %d failed out of %d tests\n", passed, failed, total))
  vim.cmd("cquit")
else
  io.write(string.format("ALL PASSED: %d tests\n", total))
  vim.cmd("qall!")
end
