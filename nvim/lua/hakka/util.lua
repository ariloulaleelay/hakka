--- Utility helpers for the hakka Neovim plugin.

local M = {}

--- Escape markdown special characters to prevent them from being interpreted
--- as formatting when the snippet is displayed outside backticks (plain text).
function M.escape_snippet(s)
  if not s then return "" end
  -- Replace literal newlines with the escaped representation "\n" so the
  -- snippet stays on one line.
  s = s:gsub("\n", "\\n")
  -- Replace literal tabs with the escaped representation "\t" so the
  -- snippet displays consistently (tab = 1 char).
  s = s:gsub("\t", "\\t")
  -- Escape markdown special characters [ and ] to prevent link formatting.
  s = s:gsub("%[", "\\["):gsub("%]", "\\]")
  -- Escape underscores and asterisks so they don't trigger italic/bold.
  s = s:gsub("_", "\\_"):gsub("%*", "\\*")
  -- Escape backticks so they don't create unintended inline code spans.
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