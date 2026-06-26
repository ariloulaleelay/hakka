--- Default shortcuts. Each key is a keymap lhs (e.g. "<CR>", "<C-s>")
--- and the value is a command name (e.g. "submit", "close").
--- Built-in command names:
---   submit   — send the prompt to the assistant
---   close    — close the chat window
---   cancel   — cancel the current in-flight request
local DEFAULT_SHORTCUTS = {
  ["<CR>"]   = "submit",
  ["<C-s>"]  = "submit",
  ["q"]      = "close",
  ["<C-c>"]  = "cancel",
}

local M = {}

M.defaults = {
  addr = "127.0.0.1:9876",
  ui = {
    width = 0.6,
    height = 0.7,
    border = "rounded",
    title = " hakka ",
  },
  -- User can override or add shortcuts:
  --   shortcuts = { ["<C-r>"] = "submit", ["<C-e>"] = "close" }
  shortcuts = {},
}

M.values = vim.deepcopy(M.defaults)

--- Merge user shortcuts on top of defaults.
--- Returns a merged table where the keys are lhs strings and values are
--- command names, with a metatable that can tell if an entry is a default.
local function merge_shortcuts(user_shortcuts)
  local result = {}
  local defaults = {}
  -- Start with defaults
  for lhs, cmd in pairs(DEFAULT_SHORTCUTS) do
    result[lhs] = cmd
    defaults[lhs] = true
  end
  -- Override/add user shortcuts
  for lhs, cmd in pairs(user_shortcuts or {}) do
    result[lhs] = cmd
    -- If the key was in defaults, it's now overridden; remove from default set
    defaults[lhs] = nil
  end
  -- Tag each entry with whether it's a user-defined shortcut
  -- We store a separate lookup for quick checking
  result._defaults = defaults
  result._user = vim.deepcopy(user_shortcuts or {})
  return result
end

function M.setup(opts)
  M.values = vim.tbl_deep_extend("force", M.defaults, opts or {})
  M.values.shortcuts = merge_shortcuts(M.values.shortcuts)
end

--- Check if a shortcut key is user-defined (i.e. overridden or added by user).
--- @param lhs string the keymap lhs
--- @return boolean
function M.is_user_shortcut(lhs)
  if not M.values or not M.values.shortcuts then
    return false
  end
  return M.values.shortcuts._user[lhs] ~= nil
end

--- Check if a shortcut key is a default (not overridden).
--- @param lhs string the keymap lhs
--- @return boolean
function M.is_default_shortcut(lhs)
  if not M.values or not M.values.shortcuts then
    return false
  end
  return M.values.shortcuts._defaults[lhs] ~= nil
end

--- Get the resolved shortcuts table (lhs → command name).
--- @return table
function M.get_shortcuts()
  if not M.values or not M.values.shortcuts then
    return {}
  end
  -- Return a copy without the internal metadata keys
  local result = {}
  for lhs, cmd in pairs(M.values.shortcuts) do
    if type(lhs) == "string" and type(cmd) == "string" then
      result[lhs] = cmd
    end
  end
  return result
end

return M
