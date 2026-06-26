--- Utility helpers for the hakka Neovim plugin.

local M = {}

--- Escape characters within user-supplied snippet text so that the bracket
--- structure of tool event lines is preserved and newlines do not break the
--- single-line markdown format.
---
--- @param s string|nil
--- @return string
function M.escape_snippet(s)
  if not s then return "" end
  -- Replace literal newlines with the escaped representation "\n" so the
  -- snippet stays on one line.
  s = s:gsub("\n", "\\n")
  -- Replace literal tabs with the escaped representation "\t" so the
  -- snippet displays consistently (tab = 1 char).
  s = s:gsub("\t", "\\t")
  -- Escape markdown special characters [ and ] to preserve the bracket
  -- structure of tool event lines.
  s = s:gsub("%[", "\\["):gsub("%]", "\\]")
  -- Escape backticks so they don't break the `name(args)` inline code format.
  s = s:gsub("`", "\\`")
  return s
end

--- Shorten a string to at most max_len characters, appending "..." if
--- truncated. This is used client-side to display tool execution snippets
--- that were sent in full from the server.
---
--- @param s string|nil
--- @param max_len number
--- @return string
function M.shorten_snippet(s, max_len)
  if not s or s == "" then return "" end
  if #s <= max_len then return s end
  return s:sub(1, max_len - 3) .. "..."
end

return M