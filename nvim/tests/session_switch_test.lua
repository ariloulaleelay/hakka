--- Test: /session switch <prefix> should work as alias for /session get

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

local function table_eq(a, b)
  if type(a) ~= type(b) then return false end
  if type(a) ~= "table" then return a == b end
  local a_keys = {}
  for k in pairs(a) do a_keys[k] = true end
  for k in pairs(b) do 
    if not a_keys[k] then return false end
    a_keys[k] = nil
  end
  for k in pairs(a_keys) do return false end
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

-- Replicate parse_slash_command from init.lua
local function parse_slash_command(text)
  local trimmed = vim.trim(text)
  if not trimmed:match("^/") then return nil end
  local parts = {}
  for part in trimmed:gmatch("%S+") do table.insert(parts, part) end
  local cmd = parts[1]
  if not cmd then return nil end
  cmd = cmd:gsub("@.*$", "")
  local known_prefixes = { "/session", "/model", "/tool", "/models" }
  for _, prefix in ipairs(known_prefixes) do
    if cmd:match("^" .. prefix .. "_") then
      local suffix = cmd:sub(#prefix + 2)
      local new_parts = { prefix, suffix }
      for i = 2, #parts do table.insert(new_parts, parts[i]) end
      parts = new_parts
      cmd = prefix
      break
    end
  end
  if cmd == "/help" then return { cmd = "help", params = {} } end
  if cmd == "/continue" then return { cmd = "continue", params = {} } end
  if cmd == "/start" then return { cmd = "start", params = {} } end
  if cmd == "/cwd_set" then
    local path = table.concat(parts, " ", 2)
    if path and path ~= "" then return { cmd = "cwd_set", params = { cwd = path } } end
    return nil
  end
  if cmd == "/compact" then
    local n = tonumber(parts[2])
    return { cmd = "compact", params = n and { n = n } or {} }
  end
  if cmd == "/session" then
    local sub = parts[2]
    if sub == "list" then return { cmd = "session_list", params = {} } end
    if sub == "create" then return { cmd = "session_create", params = {} } end
    if sub == "info" then return { cmd = "session_info", params = {} } end
    if sub == "autorename" then return { cmd = "session_autorename", params = {} } end
    if sub == "delete" then
      local target = parts[3] or ""
      return { cmd = "session_delete", params = { id = target } }
    end
    if sub == "get" then
      local target = parts[3] or ""
      return { cmd = "get_session", params = { id = target } }
    end
    if sub == "rename" then
      local name_parts = {}
      for i = 3, #parts do table.insert(name_parts, parts[i]) end
      return { cmd = "session_rename", params = { name = table.concat(name_parts, " ") } }
    end
    -- BUG: /session switch is not handled — returns nil → "Unknown command: session"
    return nil
  end
  if cmd == "/models" then return { cmd = "model_list", params = {} } end
  if cmd == "/model" then
    local sub = parts[2]
    if sub == "list" then return { cmd = "model_list", params = {} } end
    if sub == "show" then return { cmd = "session_info", params = {} } end
    if sub == "switch" or sub == "get" then
      local name = parts[3]
      if name then return { cmd = "model_switch", params = { name = name } } end
    end
    if sub and sub ~= "list" and sub ~= "show" and sub ~= "get" then
      return { cmd = "model_switch", params = { name = sub } }
    end
    return { cmd = "session_info", params = {} }
  end
  if cmd == "/tool" then
    local sub = parts[2]
    if sub == "list" then return { cmd = "tool_list", params = {} } end
    if sub == "enable" or sub == "allow" then
      local name = parts[3]
      if name then return { cmd = "tool_allow", params = { name = name } } end
    end
    if sub == "disable" or sub == "deny" then
      local name = parts[3]
      if name then return { cmd = "tool_deny", params = { name = name } } end
    end
    return nil
  end
  return nil
end

io.write("--- /session switch should be an alias for /session get ---\n")

-- This is the bug: /session switch returns nil
local r = parse_slash_command("/session switch abc123")
assert_eq(r, nil, "/session switch abc123 returns nil (BUG)")

-- /session get works
local r2 = parse_slash_command("/session get abc123")
assert_table_eq(r2, { cmd = "get_session", params = { id = "abc123" } }, "/session get abc123 works")

-- The fix should make switch an alias for get
-- Test what the fixed version should return:
-- (we'll add sub == "switch" as alias for sub == "get")
local function fixed_parse(cmd_text, sub, third)
  if cmd == "/session" then
    if sub == "switch" or sub == "get" then
      local target = third or ""
      return { cmd = "get_session", params = { id = target } }
    end
  end
end

-- Simulate what the fix would do
local function parse_fixed(text)
  local trimmed = vim.trim(text)
  if not trimmed:match("^/") then return nil end
  local parts = {}
  for part in trimmed:gmatch("%S+") do table.insert(parts, part) end
  local cmd = parts[1]
  if not cmd then return nil end
  cmd = cmd:gsub("@.*$", "")
  local known_prefixes = { "/session", "/model", "/tool", "/models" }
  for _, prefix in ipairs(known_prefixes) do
    if cmd:match("^" .. prefix .. "_") then
      local suffix = cmd:sub(#prefix + 2)
      local new_parts = { prefix, suffix }
      for i = 2, #parts do table.insert(new_parts, parts[i]) end
      parts = new_parts
      cmd = prefix
      break
    end
  end
  if cmd == "/session" then
    local sub = parts[2]
    if sub == "list" then return { cmd = "session_list", params = {} } end
    if sub == "create" then return { cmd = "session_create", params = {} } end
    if sub == "info" then return { cmd = "session_info", params = {} } end
    if sub == "autorename" then return { cmd = "session_autorename", params = {} } end
    if sub == "delete" then
      local target = parts[3] or ""
      return { cmd = "session_delete", params = { id = target } }
    end
    -- FIX: switch is alias for get (both map to get_session server command)
    if sub == "get" or sub == "switch" then
      local target = parts[3] or ""
      return { cmd = "get_session", params = { id = target } }
    end
    if sub == "rename" then
      local name_parts = {}
      for i = 3, #parts do table.insert(name_parts, parts[i]) end
      return { cmd = "session_rename", params = { name = table.concat(name_parts, " ") } }
    end
    return nil
  end
  -- rest omitted for brevity
  return nil
end

local r3 = parse_fixed("/session switch abc123")
assert_table_eq(r3, { cmd = "get_session", params = { id = "abc123" } },
  "/session switch abc123 -> get_session (after fix)")

local r4 = parse_fixed("/session switch")
assert_table_eq(r4, { cmd = "get_session", params = { id = "" } },
  "/session switch without id -> get_session with empty id (after fix)")

local r5 = parse_fixed("/session get abc123")
assert_table_eq(r5, { cmd = "get_session", params = { id = "abc123" } },
  "/session get abc123 still works (after fix)")

-- Combined underscore form
local r6 = parse_fixed("/session_switch abc123")
assert_table_eq(r6, { cmd = "get_session", params = { id = "abc123" } },
  "/session_switch abc123 (combined) -> get_session (after fix)")

io.write("\n--- Summary ---\n")
if not ok then
  io.stderr:write(string.format("FAILED: %d passed, %d failed out of %d tests\n", passed, failed, total))
  vim.cmd("cquit")
else
  io.write(string.format("ALL PASSED: %d tests\n", total))
  vim.cmd("qall!")
end
