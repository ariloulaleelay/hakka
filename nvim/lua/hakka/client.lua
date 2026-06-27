local uv = vim.uv or vim.loop

local M = {}

local function parse_addr(addr)
  local host, port = addr:match("^(.-):(%d+)$")
  if not host then
    error("hakka: invalid addr " .. tostring(addr))
  end
  return host, tonumber(port)
end

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

local function safe_encode(val)
  if val == nil then
    return "null"
  end
  local ok, encoded = pcall(vim.json.encode, val)
  if ok then
    return encoded
  end
  return '"' .. tostring(val):gsub('["\\]', function(c) return '\\' .. c end) .. '"'
end

local function connect_and_send(host, port, payload, on_frame, on_done)
  local tcp = uv.new_tcp()
  local buffer = ""
  local closed = false

  local function close(err)
    if closed then return end
    closed = true
    if tcp and not tcp:is_closing() then
      tcp:read_stop()
      tcp:close()
    end
    vim.schedule(function()
      on_done(err)
    end)
  end

  tcp:connect(host, port, function(err)
    if err then
      return close("connect: " .. err)
    end

    local req_line = vim.json.encode(payload) .. "\n"
    tcp:write(req_line, function(werr)
      if werr then
        close("write: " .. werr)
      end
    end)

    tcp:read_start(function(rerr, chunk)
      if rerr then
        return close("read: " .. rerr)
      end
      if not chunk then
        return close(nil)
      end

      buffer = buffer .. chunk
      while true do
        local nl = buffer:find("\n", 1, true)
        if not nl then break end
        local line = buffer:sub(1, nl - 1)
        buffer = buffer:sub(nl + 1)

        local ok, parsed = pcall(vim.json.decode, line)
        if not ok then
          return close("json: " .. parsed)
        end

        if parsed.event == "vim_request" and parsed.vim_request then
          local req = parsed.vim_request
          vim.schedule(function()
            local result, err = execute_vim_command(req.command)
            local resp_json = '{"type":"response","request_id":"' .. req.request_id .. '","result":' .. safe_encode(result) .. ',"error":'
            if err then
              resp_json = resp_json .. '"' .. err:gsub('["\\]', function(c) return '\\' .. c end) .. '"'
            else
              resp_json = resp_json .. 'null'
            end
            resp_json = resp_json .. '}'
            tcp:write(resp_json .. "\n")
          end)
        else
          vim.schedule(function()
            on_frame(parsed)
          end)
          if parsed.done or (parsed.error and parsed.error ~= "") then
            return close(nil)
          end
        end
      end
    end)
  end)
end

-- Non-streaming: invoke on_response(err, parsed) once.
function M.send(addr, payload, on_response)
  local host, port = parse_addr(addr)
  local got
  connect_and_send(host, port, payload,
    function(frame) got = frame end,
    function(err)
      on_response(err, got)
    end)
end

-- Streaming: on_frame(parsed) is called for each frame until done/error.
function M.stream(addr, payload, on_frame, on_done)
  local host, port = parse_addr(addr)
  payload.stream = true
  connect_and_send(host, port, payload, on_frame, on_done)
end

-- Execute a structured JSON command (non-streaming).
-- Builds the proper command frame and returns the response.
function M.execute(addr, session_id, cmd, params, on_response)
  local payload = {
    session_id = session_id or vim.NIL,
    command = { cmd = cmd, params = params or {} },
  }
  M.send(addr, payload, on_response)
end

return M
