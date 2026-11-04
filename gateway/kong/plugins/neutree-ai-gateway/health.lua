-- Instance-local, HTTP-header health. This never replays a request.
local json = require "cjson.safe"
local M = {}
local STATE = "health\0state\0"
local LOCK = "health\0lock\0"
local COOLDOWN = 15
local PROBE_LEASE = 60
local configured = {}

local function read(shared, key)
    local value = shared:get(STATE .. key)
    return value and assert(json.decode(value))
end

local function write(shared, key, value)
    return shared:safe_set(STATE .. key, assert(json.encode(value)))
end

local function token()
    return require("kong.tools.uuid").uuid()
end

-- The critical section contains only local shared-memory operations, no I/O or
-- yield. Do not hold this lock while proxying or waiting for another worker.
local function change(shared, key, apply)
    local locked, err = shared:safe_add(LOCK .. key, true, 5)
    if not locked then return nil, err == "exists" and "busy" or err end
    local result, detail = apply(read(shared, key))
    shared:delete(LOCK .. key)
    return result, detail
end

function M.configure(shared, keys)
    configured = keys
    if not shared then return end -- configure also runs before worker startup.
    for _, key in ipairs(shared:get_keys(0)) do
        if key:sub(1, #STATE) == STATE and not keys[key:sub(#STATE + 1)] then
            shared:delete(key)
        end
    end
end

function M.acquire(shared, key)
    local value = read(shared, key)
    if not value or not value.retry_at then
        return { key = key, generation = value and value.generation }
    end
    local now = ngx.now()
    if value.retry_at > now or (value.probe_until and value.probe_until > now) then
        return nil, "unavailable"
    end
    return change(shared, key, function(current)
        -- Another worker may have restored/reopened it since the read above.
        if not current or not current.retry_at then
            return { key = key, generation = current and current.generation }
        end
        if current.retry_at > now or (current.probe_until and current.probe_until > now) then
            return nil, "unavailable"
        end
        current.probe = token()
        current.probe_until = now + PROBE_LEASE
        local ok, err = write(shared, key, current)
        if not ok then return nil, err end
        return { key = key, generation = current.generation, probe = current.probe }
    end)
end

local function update(shared, admission, outcome)
    return change(shared, admission.key, function(current)
        if (current and current.generation) ~= admission.generation
            or (current and current.probe) ~= admission.probe then
            return true -- An earlier request cannot change a newer decision.
        end
        if outcome == "failure" then
            return write(shared, admission.key, {
                generation = token(), retry_at = ngx.now() + COOLDOWN,
            })
        end
        if admission.probe then
            current.probe, current.probe_until = nil, nil
            if outcome == "success" then current.retry_at = nil end
            return write(shared, admission.key, current)
        end
        return true
    end)
end

function M.record(shared, admission, outcome)
    if not configured[admission.key] then return true end
    if not admission.probe and outcome ~= "failure" then return true end
    local ok, err = update(shared, admission, outcome)
    if ok or err ~= "busy" then return ok, err end

    -- header_filter/log cannot sleep. Only contention defers the observation;
    -- ordinary failures are recorded immediately, before sending the response.
    return ngx.timer.at(0, function(premature)
        if premature then return end
        local deadline = ngx.now() + 0.1
        repeat
            if not configured[admission.key] then return end
            ok, err = update(shared, admission, outcome)
            if ok then return end
            if err ~= "busy" then break end
            ngx.sleep(0.001)
        until ngx.now() >= deadline
        kong.log.err("unable to record model target health: ", err)
    end)
end

return M
