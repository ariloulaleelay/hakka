--- Minimal WebSocket client (RFC 6455) using libuv TCP.
---
--- Supports text frames (opcode 0x1) and close frames (opcode 0x8).
--- Client-to-server frames are masked per RFC 6455 §5.3.
--- Server-to-client frames are expected unmasked.

local uv = vim.uv or vim.loop
local bit = require("bit")
local bxor = bit.bxor
local band = bit.band
local bor = bit.bor
local rshift = bit.rshift
local lshift = bit.lshift

local M = {}

-- ---------------------------------------------------------------------------
-- URL parsing
-- ---------------------------------------------------------------------------

--- Parse a WebSocket URL into components.
--- @param url string e.g. "ws://127.0.0.1:8765/ws"
--- @return string host
--- @return number port
--- @return string path
--- @return boolean ssl
local function parse_url(url)
  local scheme, host, port_str, path = url:match("^(wss?)://([^:/]+):?(%d*)(/.*)$")
  if not scheme then
    error("hakka: invalid ws url: " .. tostring(url))
  end
  local port = tonumber(port_str)
  if not port then
    port = scheme == "wss" and 443 or 80
  end
  return host, port, path, scheme == "wss"
end

-- ---------------------------------------------------------------------------
-- Random WebSocket key generation
-- ----------------------------------------------------------------------------

--- Generate a random Sec-WebSocket-Key (base64 of 16 random bytes).
--- @return string
local function generate_key()
  local bytes = uv.random(16)
  return vim.base64.encode(bytes)
end

-- ---------------------------------------------------------------------------
-- Frame encoding (client → server, always masked)
-- ---------------------------------------------------------------------------

--- Encode a text frame payload into a masked WebSocket frame.
--- @param payload string The text payload to send
--- @return string The raw frame bytes
local function encode_text_frame(payload)
  local len = #payload
  local mask = uv.random(4)
  local m1, m2, m3, m4 = string.byte(mask, 1, 4)

  -- Mask the payload using XOR with rotating mask bytes
  local masked_bytes = {}
  for i = 1, len do
    local b = string.byte(payload, i)
    local mk
    if i % 4 == 1 then mk = m1
    elseif i % 4 == 2 then mk = m2
    elseif i % 4 == 3 then mk = m3
    else mk = m4 end
    masked_bytes[i] = string.char(bxor(b, mk))
  end
  local masked_payload = table.concat(masked_bytes)

  -- Header: FIN=1, opcode=1 (text)
  local header = string.char(0x81)

  if len < 126 then
    header = header .. string.char(bor(0x80, len))
  elseif len < 65536 then
    header = header .. string.char(bor(0x80, 126))
    header = header .. string.char(rshift(len, 8), band(len, 0xFF))
  else
    -- 64-bit length (rare for our use case)
    header = header .. string.char(bor(0x80, 127))
    -- Write 8 bytes big-endian
    for i = 7, 0, -1 do
      header = header .. string.char(band(rshift(len, i * 8), 0xFF))
    end
  end

  return header .. mask .. masked_payload
end

-- ---------------------------------------------------------------------------
-- Frame parsing (server → client, unmasked)
-- ---------------------------------------------------------------------------

--- Parse a WebSocket frame from a buffer.
--- Returns the consumed bytes, the opcode, and the payload.
--- On partial data, returns nil for consumed.
--- @param buffer string
--- @return number|nil consumed bytes
--- @return number|nil opcode
--- @return string|nil payload
local function parse_frame(buffer)
  local buflen = #buffer
  if buflen < 2 then return nil end

  local b0 = string.byte(buffer, 1)
  local b1 = string.byte(buffer, 2)

  local opcode = band(b0, 0x0F)
  local masked = band(rshift(b1, 7), 1)
  local len = band(b1, 0x7F)

  local offset = 2

  if len == 126 then
    if buflen < offset + 2 then return nil end
    len = lshift(string.byte(buffer, offset + 1), 8)
        + string.byte(buffer, offset + 2)
    offset = offset + 2
  elseif len == 127 then
    if buflen < offset + 8 then return nil end
    len = 0
    for i = 0, 7 do
      len = len * 256 + string.byte(buffer, offset + 1 + i)
    end
    offset = offset + 8
  end

  local mask_key
  if masked == 1 then
    if buflen < offset + 4 then return nil end
    mask_key = buffer:sub(offset + 1, offset + 4)
    offset = offset + 4
  end

  if buflen < offset + len then return nil end

  local payload = buffer:sub(offset + 1, offset + len)

  -- Unmask if needed
  if masked == 1 and mask_key then
    local mk = { string.byte(mask_key, 1, 4) }
    local unmasked_bytes = {}
    for i = 1, len do
      local b = string.byte(payload, i)
      unmasked_bytes[i] = string.char(bxor(b, mk[(i - 1) % 4 + 1]))
    end
    payload = table.concat(unmasked_bytes)
  end

  return offset + len, opcode, payload
end

-- ---------------------------------------------------------------------------
-- Build HTTP upgrade request
-- ----------------------------------------------------------------------------

--- Build the HTTP upgrade request bytes for a WebSocket connection.
--- @param host string
--- @param port number
--- @param path string
--- @param key string The base64-encoded Sec-WebSocket-Key
--- @return string The raw HTTP request
local function build_upgrade_request(host, port, path, key)
  local lines = {
    "GET " .. path .. " HTTP/1.1",
    "Host: " .. host .. ":" .. port,
    "Upgrade: websocket",
    "Connection: Upgrade",
    "Sec-WebSocket-Key: " .. key,
    "Sec-WebSocket-Version: 13",
    "",
    "",
  }
  return table.concat(lines, "\r\n")
end

-- ---------------------------------------------------------------------------
-- Parse HTTP upgrade response
-- ----------------------------------------------------------------------------

--- Parse the HTTP upgrade response.
--- Returns status code and any extra data after headers.
--- @param buffer string The raw HTTP response bytes
--- @return number|nil status
--- @return string|nil extra
local function parse_upgrade_response(buffer)
  local header_end = buffer:find("\r\n\r\n", 1, true)
  if not header_end then return nil end

  local header = buffer:sub(1, header_end - 1)
  local first_line = header:match("^(HTTP/%d%.%d %d+)")
  if not first_line then return nil end

  local status_code = tonumber(first_line:match(" (%d+)"))
  if not status_code then return nil end

  local extra = buffer:sub(header_end + 4)
  return status_code, extra
end

-- ---------------------------------------------------------------------------
-- Connect function
-- ----------------------------------------------------------------------------

--- Connect to a WebSocket server and return a connection object.
---
--- The returned connection object has:
---   - :send(payload) — send a text frame (string or table)
---   - :close() — close the connection
---   - on_frame(cb) — set a callback for incoming text frames
---   - on_close(cb) — set a callback for connection close
---
--- Frames that arrive BEFORE on_frame is set are buffered and delivered
--- once the callback is registered.
---
--- @param url string WebSocket URL (e.g. "ws://127.0.0.1:8765/ws")
--- @param on_done fun(err: string|nil) Callback when connection established or failed
--- @return table|nil connection object
function M.connect(url, on_done)
  local host, port, path = parse_url(url)

  local tcp = uv.new_tcp()
  local buffer = ""
  local closed = false
  local handshake_done = false
  local on_frame_cb
  local on_close_cb
  -- Buffer for frames that arrive before on_frame is set
  local pending_frames = {}

  local function close(err)
    if closed then return end
    closed = true
    if tcp and not tcp:is_closing() then
      tcp:read_stop()
      tcp:close()
    end
    if on_close_cb then
      vim.schedule(function()
        on_close_cb(err)
      end)
    end
  end

  local function process_frames()
    while true do
      local consumed, opcode, payload = parse_frame(buffer)
      if not consumed then break end
      buffer = buffer:sub(consumed + 1)

      if opcode == 0x1 then
        -- Text frame
        if on_frame_cb then
          vim.schedule(function()
            on_frame_cb(payload)
          end)
        else
          -- Buffer the frame for later delivery
          pending_frames[#pending_frames + 1] = payload
        end
      elseif opcode == 0x8 then
        -- Close frame
        close(nil)
        return
      elseif opcode == 0x9 then
        -- Ping — send pong
        local pong_frame = string.char(0x8A, #payload) .. payload
        tcp:write(pong_frame)
      elseif opcode == 0xA then
        -- Pong — ignore
      end
    end
  end

  local function deliver_pending_frames()
    local frames = pending_frames
    pending_frames = {}
    for _, text in ipairs(frames) do
      vim.schedule(function()
        if on_frame_cb then
          on_frame_cb(text)
        end
      end)
    end
  end

  local connection = {
    --- Send a text frame.
    --- @param payload string|table Text string or JSON-serializable table
    send = function(self, payload)
      local text
      if type(payload) == "table" then
        text = vim.json.encode(payload)
      else
        text = tostring(payload)
      end
      local frame = encode_text_frame(text)
      tcp:write(frame)
    end,

    --- Close the WebSocket connection.
    close = function(self)
      close(nil)
    end,

    --- Set callback for incoming text frames.
    --- Delivers any buffered frames immediately.
    --- @param cb fun(payload: string)
    on_frame = function(self, cb)
      on_frame_cb = cb
      deliver_pending_frames()
    end,

    --- Set callback for connection close.
    --- @param cb fun(err: string|nil)
    on_close = function(self, cb)
      on_close_cb = cb
    end,
  }

  tcp:connect(host, port, function(err)
    if err then
      return close("connect: " .. err)
    end

    -- Send HTTP upgrade request
    local key = generate_key()
    local upgrade_req = build_upgrade_request(host, port, path, key)
    tcp:write(upgrade_req, function(werr)
      if werr then
        return close("write upgrade: " .. werr)
      end
    end)

    tcp:read_start(function(rerr, chunk)
      if rerr then
        return close("read: " .. rerr)
      end
      if not chunk then
        return close(nil) -- clean EOF
      end

      buffer = buffer .. chunk

      if not handshake_done then
        local status, extra = parse_upgrade_response(buffer)
        if status then
          if status ~= 101 then
            return close("upgrade failed: HTTP " .. status)
          end
          handshake_done = true
          buffer = extra or ""

          -- Notify that connection is established
          vim.schedule(function()
            if on_done then on_done(nil) end
          end)

          -- Process any data that came after the upgrade response
          if #buffer > 0 then
            process_frames()
          end
        end
        return
      end

      process_frames()
    end)
  end)

  return connection
end

return M
