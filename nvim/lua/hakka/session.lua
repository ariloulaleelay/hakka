--- Hakka Neovim plugin — session state management.
---
--- Manages the current session ID, name, model, and provides lazy
--- session creation. Uses the persistent connection from connection.lua
--- to send commands.
---
--- Typical usage:
---   session.ensure(cb)   -- creates a session lazily if none exists
---   session.current()    -- returns {id, name, model} or nil
---   session.switch(id)   -- switches to another session
---   session.reset()      -- clears current session (creates new on next ensure)
---   session.set_from_response(frame)  -- updates state from server frames

local M = {}

local connection = nil
local state = {
  id = nil,
  name = nil,
  model = nil,
}

--- Initialize the session module with a connection reference.
--- Must be called before any other function.
--- @param opts table with `connection` field
function M.setup(opts)
  connection = opts.connection
end

--- Return the current session info or nil.
--- @return table|nil {id, name, model}
function M.current()
  if not state.id then
    return nil
  end
  return {
    id = state.id,
    name = state.name,
    model = state.model,
  }
end

--- Ensure a session exists. Creates one lazily if none is active.
--- Calls callback(err, session_id) when done.
--- @param callback fun(err: string|nil, session_id: string|nil)
function M.ensure(callback)
  if state.id then
    if callback then
      vim.schedule(function()
        callback(nil, state.id)
      end)
    end
    return
  end

  -- No active session — create one
  connection.execute(nil, "session_create", {}, function(err, resp)
    if err then
      if callback then callback(err, nil) end
      return
    end
    if not resp then
      if callback then callback("no response from server", nil) end
      return
    end

    -- Extract session info from the response
    M.set_from_response(resp)

    if callback then
      callback(nil, state.id)
    end
  end)
end

--- Switch to another session by ID.
--- Fetches the session data and sets it as current.
--- @param id string Session ID or prefix
--- @param callback fun(err: string|nil, session_id: string|nil)
function M.switch(id, callback)
  if not id or id == "" then
    if callback then callback("no session ID specified", nil) end
    return
  end

  connection.execute(nil, "get_session", { id = id }, function(err, resp)
    if err then
      if callback then callback(err, nil) end
      return
    end
    if not resp then
      if callback then callback("no response from server", nil) end
      return
    end

    -- Extract session info from the response
    M.set_from_response(resp)

    if callback then
      callback(nil, state.id)
    end
  end)
end

--- Clear the current session. Next ensure() will create a new one.
function M.reset()
  state.id = nil
  state.name = nil
  state.model = nil
end

--- Delete a session by ID. If it's the current session, resets.
--- @param id string Session ID or prefix
--- @param callback fun(err: string|nil, msg: string|nil)
function M.delete(id, callback)
  connection.execute(nil, "session_delete", { id = id }, function(err, resp)
    if err then
      if callback then callback(err, nil) end
      return
    end
    if not resp then
      if callback then callback("no response from server", nil) end
      return
    end

    local msg = resp.data and resp.data.deleted or id
    local active_cleared = resp.data and resp.data.active_cleared

    if active_cleared then
      M.reset()
    end

    if callback then
      callback(nil, "Session " .. (msg or "?") .. " deleted")
    end
  end)
end

--- Fetch the session list from the server.
--- @param callback fun(sessions: table|nil)
function M.list(callback)
  connection.execute(nil, "session_list", {}, function(err, resp)
    if err or not resp or not resp.data then
      if callback then callback(nil) end
      return
    end
    if callback then
      callback(resp.data.sessions)
    end
  end)
end

--- Update internal state from a server frame.
--- Handles "session" type frames (session_create, get_session) and
--- "result" type frames that contain session data.
--- @param frame table A parsed server frame
function M.set_from_response(frame)
  if not frame then return end

  local sid, sname, smodel

  if frame.type == "session" then
    -- type="session" with event="session_create"|"get_session"
    -- Session data is at frame.session
    if frame.session then
      sid = frame.session.id
      sname = frame.session.name
      smodel = frame.session.model
    end
  elseif frame.type == "result" then
    -- type="result" with cmd="session_create"|"start"
    -- Session data is at frame.data.session
    if frame.data and frame.data.session then
      sid = frame.data.session.id
      sname = frame.data.session.name
      smodel = frame.data.session.model
    end
  end

  if sid then
    state.id = sid
    state.name = sname or ""
    state.model = smodel or "?"
  end
end

return M
