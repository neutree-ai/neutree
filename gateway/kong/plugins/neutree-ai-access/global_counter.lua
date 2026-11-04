-- Rate-limit windows counted in PostgreSQL, so every Kong pod shares one
-- counter per key and window instead of each pod counting on its own.
--
-- Off unless NEUTREE_GLOBAL_RATE_LIMIT=on (the env must be declared to nginx
-- through KONG_NGINX_MAIN_ENV). Any failure opens a breaker for a cooldown, and
-- the caller counts in the per-pod shared dict meanwhile, so an unavailable
-- database never makes limiting stricter or requests slower than the timeout.

local _M = {}

local TABLE = "neutree_gateway.rate_limit_counters"
local POOL = "neutree_global_rate_limit"
local POOL_SIZE = 10
local KEEPALIVE_MS = 60000
-- Only windows the cleanup job can expire by window_start alone.
_M.WINDOWS = { second = 1, minute = 60 }

local settings

local function positive_env(name, default)
    local v = tonumber(os.getenv(name) or "")
    if v and v > 0 then
        return v
    end
    return default
end

local function load_settings()
    if not settings then
        settings = {
            enabled = os.getenv("NEUTREE_GLOBAL_RATE_LIMIT") == "on",
            timeout_ms = positive_env("NEUTREE_GLOBAL_RATE_LIMIT_TIMEOUT_MS", 150),
            cooldown = positive_env("NEUTREE_GLOBAL_RATE_LIMIT_COOLDOWN_SECONDS", 10),
        }
    end
    return settings
end

-- Per worker: each worker pays at most one timeout per cooldown.
local open_until = 0

function _M.available()
    return load_settings().enabled and ngx.now() >= open_until
end

local function trip(err)
    local s = load_settings()
    open_until = ngx.now() + s.cooldown
    kong.log.warn("neutree-ai-access: global rate limit unavailable, counting per pod for ",
        s.cooldown, "s: ", tostring(err))
end

-- build_sql increments the windows in order and stops at the first one over
-- its limit, like the per-pod loop: a request rejected per second must not
-- also use up the per-minute budget. Window boundaries come from the database
-- clock so pods with skewed clocks agree on them.
local function build_sql(pg, key, windows)
    local ctes, selects = {}, {}
    for i, w in ipairs(windows) do
        local k = pg:escape_literal(key .. ":" .. w.name)
        local ws = string.format("to_timestamp(floor(extract(epoch from now()) / %d) * %d)", w.seconds, w.seconds)
        local source
        if i == 1 then
            source = string.format("VALUES (%s, %s, 1)", k, ws)
        else
            source = string.format("SELECT %s, %s, 1 FROM w%d WHERE w%d.hits <= %d", k, ws, i - 1, i - 1, w.prev_limit)
        end
        ctes[#ctes + 1] = string.format(
            "w%d AS (INSERT INTO %s AS c (counter_key, window_start, hits) %s "
                .. "ON CONFLICT (counter_key, window_start) DO UPDATE SET hits = c.hits + 1 "
                .. "RETURNING hits, extract(epoch from window_start)::float8 AS window_start)",
            i, TABLE, source)
        selects[#selects + 1] = string.format(
            "SELECT %d AS i, hits, window_start, extract(epoch from now())::float8 AS now FROM w%d", i, i)
    end
    -- Commit without waiting for a synchronous standby: the table is unlogged,
    -- and a lost window only resets a counter.
    return "BEGIN; SET LOCAL synchronous_commit = local; WITH "
        .. table.concat(ctes, ", ") .. " " .. table.concat(selects, " UNION ALL ") .. "; COMMIT;"
end

local function connect()
    local c = kong.configuration
    local pg = require("pgmoon").new({
        host = c.pg_host,
        port = c.pg_port,
        database = c.pg_database,
        user = c.pg_user,
        password = c.pg_password,
        ssl = c.pg_ssl,
        ssl_verify = c.pg_ssl_verify,
        pool_name = POOL,
        pool_size = POOL_SIZE,
        socket_type = "nginx",
    })
    pg:settimeout(load_settings().timeout_ms)
    local ok, err = pg:connect()
    if not ok then
        return nil, err
    end
    return pg
end

local function rows_of(res)
    -- One result per statement: BEGIN, SET, the counters, COMMIT.
    if type(res) == "table" then
        for _, r in ipairs(res) do
            if type(r) == "table" and type(r[1]) == "table" and r[1].hits ~= nil then
                return r
            end
        end
    end
    return nil
end

-- hit counts one request in each window, in order. windows is a list of
-- { name, seconds, limit }. It returns one entry per window: { hits,
-- window_start, now } in epoch seconds, or false for a window not reached
-- because an earlier one was already over its limit. On any failure it returns
-- nil and the caller counts per pod.
function _M.hit(key, windows)
    local list = {}
    for i, w in ipairs(windows) do
        list[i] = { name = w.name, seconds = w.seconds, prev_limit = i > 1 and windows[i - 1].limit or nil }
    end

    local pg, err = connect()
    if not pg then
        trip(err)
        return nil
    end

    local res, qerr = pg:query(build_sql(pg, key, list))
    local rows = res and rows_of(res)
    if not rows then
        -- The connection may be inside an aborted transaction; never pool it.
        pg:disconnect()
        trip(qerr or "unexpected result")
        return nil
    end
    pg:keepalive(KEEPALIVE_MS, POOL_SIZE)

    local out = {}
    for i = 1, #windows do
        out[i] = false
    end
    for _, r in ipairs(rows) do
        out[tonumber(r.i)] = {
            hits = tonumber(r.hits),
            window_start = tonumber(r.window_start),
            now = tonumber(r.now),
        }
    end
    return out
end

-- For tests.
function _M._reset()
    settings = nil
    open_until = 0
end

return _M
