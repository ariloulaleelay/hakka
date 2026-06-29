local config = require("hakka.config")

local M = {}

local state = {
  buf = nil,
  win = nil,
  prompt_start = nil,
}

--- Holds the session ID and model info displayed in the status bar.
local info = {
  session_id = nil,
  model = "?",
}

local function lines(buf)
  return vim.api.nvim_buf_get_lines(buf, 0, -1, false)
end

--- Safely set buffer lines, handling strings that contain embedded newlines.
--- Neovim's nvim_buf_set_lines rejects elements with \n, so we split them.
--- @param buf number Buffer handle
--- @param arr table Array of strings (any element may contain \n)
local function set_lines(buf, arr)
  local flat = {}
  for _, line in ipairs(arr) do
    if type(line) == "string" and line:find("\n") then
      for _, part in ipairs(vim.split(line, "\n", { plain = true })) do
        table.insert(flat, part)
      end
    else
      table.insert(flat, line)
    end
  end
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, flat)
end

--- Append text to the buffer. The buffer must be modifiable.
--- The first incoming line is concatenated to the last existing line (which
--- is expected to be a blank placeholder). Subsequent lines are appended.
local function append(buf, text)
  vim.api.nvim_buf_set_option(buf, "modifiable", true)
  local existing = lines(buf)
  if #existing == 0 then
    -- Edge case: empty buffer; just set the lines.
    local incoming = vim.split(text, "\n", { plain = true })
    vim.api.nvim_buf_set_lines(buf, 0, -1, false, incoming)
    return
  end
  local last = existing[#existing]
  local incoming = vim.split(text, "\n", { plain = true })
  existing[#existing] = last .. incoming[1]
  for i = 2, #incoming do
    table.insert(existing, incoming[i])
  end
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, existing)

  if state.win and vim.api.nvim_win_is_valid(state.win) then
    local line_count = vim.api.nvim_buf_line_count(buf)
    vim.api.nvim_win_set_cursor(state.win, { line_count, 0 })
  end
end

--- Reset the buffer to show a fresh prompt after an assistant response.
local function reset_prompt(buf)
  vim.api.nvim_buf_set_option(buf, "modifiable", true)
  local existing = lines(buf)
  if #existing > 0 and existing[#existing] ~= "" then
    table.insert(existing, "")
  end
  table.insert(existing, "# Me")
  table.insert(existing, "")
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, existing)
  state.prompt_start = #existing
  vim.api.nvim_win_set_cursor(state.win, { state.prompt_start, 0 })
end

--- Read the prompt text from the buffer (lines after prompt_start).
local function read_prompt(buf)
  local all = lines(buf)
  if not state.prompt_start then
    return ""
  end
  local parts = {}
  for i = state.prompt_start, #all do
    table.insert(parts, all[i])
  end
  return table.concat(parts, "\n"):gsub("^%s+", ""):gsub("%s+$", "")
end

--- Update the status line with session info.
local function update_status()
  if not state.buf or not vim.api.nvim_buf_is_valid(state.buf) then
    return
  end
  local parts = {}
  if info.session_id then
    table.insert(parts, "session: " .. info.session_id:sub(1, 8))
  end
  if info.model and info.model ~= "?" then
    table.insert(parts, "model: " .. info.model)
  end
  local status = #parts > 0 and table.concat(parts, "  ") or ""
  vim.g.hakka_status = status
  vim.api.nvim_exec_autocmds("User", { pattern = "HakkaStatusUpdated" })
end

function M.is_open()
  return state.win and vim.api.nvim_win_is_valid(state.win)
end

function M.close()
  if M.is_open() then
    vim.api.nvim_win_close(state.win, true)
  end
  state.win = nil
end

--- Build a human-readable shortcut help text for display.
--- Returns a string like:
---   "<CR>         submit  (default)"
---   "<C-s>        submit  (user)"
---   "q            close   (default)"
---   "?            shortcuts help"
local function shortcuts_help_text()
  local shortcuts = config.get_shortcuts()
  local lines_out = {}
  -- Sort keys for consistent display
  local keys = {}
  for lhs, _ in pairs(shortcuts) do
    table.insert(keys, lhs)
  end
  table.sort(keys, function(a, b)
    -- Sort so that alphabetical keys go first, then special keys
    local a_alpha = a:match("^[a-zA-Z]$")
    local b_alpha = b:match("^[a-zA-Z]$")
    if a_alpha and not b_alpha then return true end
    if not a_alpha and b_alpha then return false end
    return a < b
  end)

  for _, lhs in ipairs(keys) do
    local cmd = shortcuts[lhs]
    local tag = config.is_user_shortcut(lhs) and "(user)" or "(default)"
    table.insert(lines_out, string.format("  %-14s %-10s %s", lhs, cmd, tag))
  end
  return table.concat(lines_out, "\n")
end

--- Show a floating window listing all available shortcuts.
local function show_shortcuts_popup()
  local help_text = shortcuts_help_text()
  if help_text == "" then
    help_text = "  (no shortcuts configured)"
  end

  local buf = vim.api.nvim_create_buf(false, true)
  local lines_list = vim.split(help_text, "\n", { plain = true })
  -- Add a title line
  table.insert(lines_list, 1, " Hakka Shortcuts ")
  table.insert(lines_list, 2, "")
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines_list)

  local width = 40
  local height = #lines_list + 2
  local row = math.floor((vim.o.lines - height) / 2)
  local col = math.floor((vim.o.columns - width) / 2)

  local win = vim.api.nvim_open_win(buf, true, {
    relative = "editor",
    width = width,
    height = height,
    row = row,
    col = col,
    style = "minimal",
    border = "rounded",
    title = " Shortcuts ",
    title_pos = "center",
  })

  -- Highlight user-defined shortcuts
  vim.cmd("highlight default HakkaUserShortcut guifg=#ffaa00")
  vim.cmd("highlight default HakkaDefaultShortcut guifg=#888888")

  local ns = vim.api.nvim_create_namespace("hakka_shortcuts")
  for i, line in ipairs(lines_list) do
    -- Find which shortcut this line corresponds to
    local lhs = line:match("^%s+(%S+)")
    if lhs then
      local hl_group = config.is_user_shortcut(lhs) and "HakkaUserShortcut" or "HakkaDefaultShortcut"
      -- Highlight the tag at the end
      local tag_start = line:find("%((default)%)") or line:find("%((user)%)")
      if tag_start then
        vim.api.nvim_buf_add_highlight(buf, ns, hl_group, i - 1, tag_start - 1, #line)
      end
    end
  end

  -- Close on any key
  vim.api.nvim_buf_set_keymap(buf, "n", "q", "<cmd>close<CR>", { nowait = true, silent = true })
  vim.api.nvim_buf_set_keymap(buf, "n", "<CR>", "<cmd>close<CR>", { nowait = true, silent = true })
  vim.api.nvim_buf_set_keymap(buf, "n", "<Esc>", "<cmd>close<CR>", { nowait = true, silent = true })
end

--- Apply shortcuts from config to the buffer.
--- This is called each time the window opens.
--- @param on_send function(text) called when the user submits the prompt
--- @param on_cancel function() called when the user cancels an in-flight request
local function apply_shortcuts(on_send, on_cancel)
  if not state.buf then return end

  local shortcuts = config.get_shortcuts()
  local submit_fn

  for lhs, cmd in pairs(shortcuts) do
    if cmd == "submit" then
      -- Build submit function lazily (needs on_send which is known at open time)
      if not submit_fn then
        submit_fn = function()
          local text = read_prompt(state.buf)
          if text == "" then
            return
          end
          on_send(text)
        end
      end

      -- Determine modes: if lhs contains "<C-" it's probably insert mode,
      -- otherwise if it's a single letter it's normal mode, for others try both
      local modes = { "n" }
      if lhs:find("<") and lhs ~= "<CR>" then
        -- Special keys like <CR>, <C-s> — apply in both normal and insert
        modes = { "n", "i" }
      end

      for _, mode in ipairs(modes) do
        local rhs = submit_fn
        if mode == "i" then
          rhs = function()
            vim.cmd("stopinsert")
            submit_fn()
          end
        end
        pcall(vim.keymap.set, mode, lhs, rhs, { buffer = state.buf, nowait = true })
      end
    elseif cmd == "close" then
      vim.keymap.set("n", lhs, function()
        M.close()
      end, { buffer = state.buf, nowait = true })
    elseif cmd == "cancel" then
      if on_cancel then
        local modes = { "n", "i" }
        for _, mode in ipairs(modes) do
          pcall(vim.keymap.set, mode, lhs, function()
            on_cancel()
          end, { buffer = state.buf, nowait = true })
        end
      end
    end
  end

  -- Always add the "?" shortcut to show the shortcuts help popup
  vim.keymap.set("n", "?", show_shortcuts_popup, { buffer = state.buf, nowait = true, desc = "show shortcuts" })
end

function M.open(on_send, on_cancel)
  if M.is_open() then
    vim.api.nvim_set_current_win(state.win)
    return
  end

  if not state.buf or not vim.api.nvim_buf_is_valid(state.buf) then
    state.buf = vim.api.nvim_create_buf(false, true)
    vim.api.nvim_buf_set_option(state.buf, "bufhidden", "hide")
    vim.api.nvim_buf_set_option(state.buf, "filetype", "markdown")
    -- Two separate lines: header then blank line for input.
    -- The blank line ensures cursor starts on its own line,
    -- not on the header line.
    vim.api.nvim_buf_set_lines(state.buf, 0, -1, false, { "# Me", "" })
    state.prompt_start = 2
  end

  local ui = config.values.ui
  local w = math.floor(vim.o.columns * ui.width)

  vim.cmd("botright vsplit")
  state.win = vim.api.nvim_get_current_win()
  vim.api.nvim_win_set_buf(state.win, state.buf)
  vim.api.nvim_win_set_width(state.win, w)

  vim.api.nvim_win_set_option(state.win, "wrap", true)
  vim.api.nvim_win_set_option(state.win, "linebreak", true)

  -- Show session/model info as window title
  local title_parts = { ui.title or " hakka " }
  if info.session_id then
    table.insert(title_parts, "[" .. info.session_id:sub(1, 8) .. "]")
  end
  if info.model and info.model ~= "?" then
    table.insert(title_parts, info.model)
  end
  vim.wo[state.win].winbar = table.concat(title_parts, " ")

  -- Apply shortcuts from config (including user overrides)
  apply_shortcuts(on_send, on_cancel)

  -- Scroll to bottom so the prompt is visible
  local line_count = vim.api.nvim_buf_line_count(state.buf)
  vim.api.nvim_win_set_cursor(state.win, { line_count, 0 })

  vim.cmd("startinsert!")
end

function M.append_user(text)
  if not state.buf then return end
  vim.api.nvim_buf_set_option(state.buf, "modifiable", true)
  local all = lines(state.buf)
  if state.prompt_start then
    while #all >= state.prompt_start do
      table.remove(all, #all)
    end
  end
  local incoming = vim.split(text, "\n", { plain = true })
  for _, line in ipairs(incoming) do
    table.insert(all, line)
  end
  set_lines(state.buf, all)
  if state.win and vim.api.nvim_win_is_valid(state.win) then
    vim.api.nvim_win_set_cursor(state.win, { #all, 0 })
  end
end

function M.append_assistant(text)
  if not state.buf then return end
  append(state.buf, "# Hakka\n" .. text)
  reset_prompt(state.buf)
end

local streaming = false

function M.begin_assistant_stream()
  if not state.buf then return end
  vim.api.nvim_buf_set_option(state.buf, "modifiable", true)
  local all = lines(state.buf)
  -- Ensure a blank line before the assistant header
  if #all > 0 and all[#all] ~= "" then
    table.insert(all, "")
  end
  table.insert(all, "# Hakka")
  table.insert(all, "")
  set_lines(state.buf, all)
  if state.win and vim.api.nvim_win_is_valid(state.win) then
    vim.api.nvim_win_set_cursor(state.win, { #all, 0 })
  end
  streaming = true
end

function M.append_delta(text)
  if not state.buf or not streaming or not vim.api.nvim_buf_is_valid(state.buf) then return end
  append(state.buf, text)
end

function M.end_assistant_stream()
  streaming = false
  if not state.buf or not vim.api.nvim_buf_is_valid(state.buf) then return end
  reset_prompt(state.buf)
end

local function ensure_new_line()
  vim.api.nvim_buf_set_option(state.buf, "modifiable", true)
  local all = lines(state.buf)
  if #all == 0 or all[#all] ~= "" then
    table.insert(all, "")
    set_lines(state.buf, all)
  end
end

local util = require("hakka.util")

--- Append a tool event to the buffer.
--- @param name string Tool name
--- @param status string "start", "ok", or "err"
--- @param snippet string Human-readable exec snippet (e.g. 'read_file("/tmp/foo.txt")')
--- @param data table|nil Optional data from server, may contain "result" with tool output
function M.append_tool_event(name, status, snippet, data)
  if not state.buf or not vim.api.nvim_buf_is_valid(state.buf) then return end
  vim.api.nvim_buf_set_option(state.buf, "modifiable", true)

  -- Compute dynamic max length for the snippet.
  local win_width = vim.o.columns
  if state.win and vim.api.nvim_win_is_valid(state.win) then
    win_width = vim.api.nvim_win_get_width(state.win)
  end
  local max_line = math.min(100, win_width)
  -- Overhead: backtick(1) + name + backtick(1) + space(1) = 3 + len(name)
  local snippet_max = math.max(10, max_line - #name - 3)

  -- Escape then shorten.
  local safe = util.shorten_snippet(util.escape_snippet(snippet), snippet_max)

  if status == "start" then
    ensure_new_line()
    append(state.buf, "`" .. name .. "` " .. safe)
    if streaming then
      ensure_new_line()
    end
  else
    -- On completion, find the matching pending line and update its snippet.
    local all = lines(state.buf)
    local line_prefix = "`" .. name .. "` "
    local found = false
    -- result omitted: LLM response stream shows it

    for i = #all, 1, -1 do
      if all[i]:sub(1, #line_prefix) == line_prefix then
        -- Show only the tool name and snippet -- result is visible
        -- in the LLM response stream, no need to duplicate it.
        all[i] = "`" .. name .. "` " .. safe
        found = true
        break
      end
    end

    if found then
      set_lines(state.buf, all)
    else
      -- Fallback: append a new completed line
      ensure_new_line()
      append(state.buf, "`" .. name .. "` " .. safe)
      if streaming then
        ensure_new_line()
      end
    end
  end
end

function M.append_meta_event(data)
  local ctx = data.prompt_tokens or 0
  local total = data.total_tokens or 0
  vim.g.hakka_tokens = string.format("%d ctx / %d req", ctx, total)
  vim.api.nvim_exec_autocmds("User", { pattern = "HakkaTokensUpdated" })
end

function M.append_error(text)
  if not state.buf or not vim.api.nvim_buf_is_valid(state.buf) then return end
  append(state.buf, "_error: " .. text .. "_")
  reset_prompt(state.buf)
end

--- Set the session ID for display. Called by init.lua when a session is
--- created or switched.
function M.set_session_id(id)
  info.session_id = id
  update_status()
end

--- Set the model name for display.
function M.set_model(name)
  info.model = name or "?"
  update_status()
end

--- Clear the buffer content and reset to initial state. Used when
--- switching sessions.
function M.clear()
  if not state.buf then return end
  vim.api.nvim_buf_set_option(state.buf, "modifiable", true)
  vim.api.nvim_buf_set_lines(state.buf, 0, -1, false, { "# Me", "" })
  state.prompt_start = 2
  streaming = false
  if state.win and vim.api.nvim_win_is_valid(state.win) then
    local line_count = vim.api.nvim_buf_line_count(state.buf)
    vim.api.nvim_win_set_cursor(state.win, { line_count, 0 })
  end
end

--- Load full session history into the buffer.
--- Renders all past messages so the user sees the complete conversation.
--- @param messages table Array of {role, content, ...} objects
function M.load_history(messages)
  if not state.buf then return end
  vim.api.nvim_buf_set_option(state.buf, "modifiable", true)

  local lines = {}
  local last_role = nil
  for _, msg in ipairs(messages or {}) do
    if msg.role == "user" then
      local content = (msg.content or ""):gsub("^%s+", ""):gsub("%s+$", "")
      if content ~= "" then
        if last_role ~= "user" and #lines > 0 and lines[#lines] ~= "" then
          table.insert(lines, "")
        end
        if last_role ~= "user" then
          table.insert(lines, "# Me")
          table.insert(lines, "")
        end
        last_role = "user"
        for _, content_line in ipairs(vim.split(content, "\n", { plain = true })) do
          table.insert(lines, content_line)
        end
      end
    elseif msg.role == "assistant" then
      local content = (msg.content or ""):gsub("^%s+", ""):gsub("%s+$", "")
      if content ~= "" then
        if last_role ~= "assistant" and #lines > 0 and lines[#lines] ~= "" then
          table.insert(lines, "")
        end
        if last_role ~= "assistant" then
          table.insert(lines, "# Hakka")
          table.insert(lines, "")
        end
        last_role = "assistant"
        for _, content_line in ipairs(vim.split(content, "\n", { plain = true })) do
          table.insert(lines, content_line)
        end
      end
    -- tool messages are omitted — they contain internal function call
    -- results and are not meaningful in user-facing history.
    end
  end

  -- Set the rendered history in the buffer
  set_lines(state.buf, lines)

  -- Add a fresh user prompt after history.
  if last_role == "user" then
    table.insert(lines, "")
    set_lines(state.buf, lines)
    state.prompt_start = #lines
  else
    state.prompt_start = #lines + 2
    table.insert(lines, "")
    table.insert(lines, "# Me")
    table.insert(lines, "")
    set_lines(state.buf, lines)
  end

  if state.win and vim.api.nvim_win_is_valid(state.win) then
    vim.api.nvim_win_set_cursor(state.win, { #lines, 0 })
  end

  streaming = false
end

--- Expose show_shortcuts_popup for testing/programmatic use.
function M.show_shortcuts_help()
  show_shortcuts_popup()
end

return M
