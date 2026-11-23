-- Unit tests for neutree-ai-access rate limits: the per-pod shared dict, and
-- the PostgreSQL counter shared by all pods (global_counter.lua) with its
-- breaker and per-pod fallback. pgmoon and the PDK are stubbed.
-- Run: make gateway-lua-test

local clock = 1000.25
local env = {}
local real_getenv = os.getenv

-- In-memory stand-in for ngx.shared.kong_rate_limiting_counters.
local function new_dict()
    local data = {}
    return {
        data = data,
        add = function(_, k, v)
            if data[k] ~= nil then
                return false, "exists"
            end
            data[k] = v
            return true
        end,
        incr = function(_, k, by, init)
            if data[k] == nil then
                if init == nil then
                    return nil, "not found"
                end
                data[k] = init
            end
            data[k] = data[k] + by
            return data[k]
        end,
    }
end

_G.ngx = {
    now = function() return clock end,
    update_time = function() end,
    shared = {},
}

local denied, headers, warnings
_G.kong = {
    request = {
        get_method = function() return "POST" end,
        get_path = function() return "/v1/chat/completions" end,
        get_raw_body = function() return nil end,
    },
    client = { get_consumer = function() return { custom_id = "key-1" } end },
    ctx = { shared = {}, plugin = {} },
    response = {
        exit = function(status, body) denied = { status = status, body = body } end,
        set_header = function(name, value) headers[name] = value end,
    },
    log = {
        warn = function(...) warnings[#warnings + 1] = table.concat({ ... }) end,
        err = function() end,
        debug = function() end,
    },
    configuration = {
        pg_host = "db", pg_port = 5432, pg_database = "neutree",
        pg_user = "kong_admin", pg_password = "pw",
    },
}

-- Fake pgmoon: `db` scripts how the next connection behaves.
local db
local function reset_db()
    db = {
        connects = 0, queries = {}, pooled = 0, closed = 0,
        connect_err = nil, query_err = nil,
        -- rows the counter statement returns
        rows = {},
        -- runs while a statement is in flight, to stand in for concurrent requests
        on_query = nil,
    }
end

package.preload["pgmoon"] = function()
    return {
        new = function(config)
            db.config = config
            return {
                sock = { getreusedtimes = function() return 0 end },
                settimeout = function(_, ms) db.timeout = ms end,
                connect = function()
                    db.connects = db.connects + 1
                    if db.connect_err then
                        return nil, db.connect_err
                    end
                    return true
                end,
                escape_literal = function(_, v) return "'" .. v:gsub("'", "''") .. "'" end,
                query = function(_, sql)
                    db.queries[#db.queries + 1] = sql
                    local hook = db.on_query
                    db.on_query = nil
                    if hook then
                        hook()
                    end
                    if db.query_err then
                        return nil, db.query_err
                    end
                    return { true, true, db.rows, true }, 4
                end,
                keepalive = function() db.pooled = db.pooled + 1 end,
                disconnect = function() db.closed = db.closed + 1 end,
            }
        end,
    }
end

local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)") or "./"
local global_counter = assert(loadfile(spec_dir .. "../global_counter.lua"))()
package.preload["kong.plugins.neutree-ai-access.global_counter"] = function() return global_counter end
local handler = assert(loadfile(spec_dir .. "../handler.lua"))()

local RPS_RPM = { { limit = 2, window = "second" }, { limit = 60, window = "minute" } }

local function run(rate_limits, concurrency)
    denied = nil
    headers = {}
    kong.ctx.plugin = {}
    handler:access({ disabled = false, concurrency = concurrency or 0, rate_limits = rate_limits })
    return denied
end

local function assert_rate_limited(res)
    assert.is_not_nil(res, "expected a 429")
    assert.are.equal(429, res.status)
    assert.are.equal("rate_limit_exceeded", res.body.error.code)
    assert.are.equal("rate_limited", res.body.error.type)
end

local function setup_env(vars)
    env = vars
    os.getenv = function(name) return env[name] end
    global_counter._reset()
end

before_each(function()
    clock = 1000.25
    warnings = {}
    reset_db()
    ngx.shared.kong_rate_limiting_counters = new_dict()
end)

after_each(function()
    os.getenv = real_getenv
end)

describe("neutree-ai-access rate limits with the global counter off", function()
    before_each(function() setup_env({}) end)

    it("counts in the shared dict and never opens a database connection", function()
        assert.is_nil(run(RPS_RPM))
        assert.is_nil(run(RPS_RPM))
        local res = run(RPS_RPM)
        assert_rate_limited(res)
        assert.are.equal(1, headers["Retry-After"])
        assert.are.equal(0, db.connects)
    end)

    it("does not count the minute window for a request the second window rejected", function()
        for _ = 1, 5 do
            run(RPS_RPM)
        end
        local data = ngx.shared.kong_rate_limiting_counters.data
        assert.are.equal(5, data["neutree_rl:key-1:second:1000"])
        assert.are.equal(2, data["neutree_rl:key-1:minute:960"])
    end)

    it("starts a new count in the next fixed window", function()
        run(RPS_RPM); run(RPS_RPM)
        assert_rate_limited(run(RPS_RPM))
        clock = 1001.0
        assert.is_nil(run(RPS_RPM))
    end)

    it("releases the concurrency slot of a rate-limited request", function()
        run(RPS_RPM, 10); run(RPS_RPM, 10)
        assert_rate_limited(run(RPS_RPM, 10))
        -- log() was not run for the first two, so only they hold slots.
        assert.are.equal(2, ngx.shared.kong_rate_limiting_counters.data["neutree_cc:key-1"])
    end)
end)

describe("neutree-ai-access rate limits with the global counter on", function()
    before_each(function()
        setup_env({
            NEUTREE_GLOBAL_RATE_LIMIT = "on",
            NEUTREE_GLOBAL_RATE_LIMIT_TIMEOUT_MS = "120",
            NEUTREE_GLOBAL_RATE_LIMIT_COOLDOWN_SECONDS = "5",
        })
    end)

    it("counts both windows in one statement that commits without a sync standby", function()
        db.rows = {
            { i = 1, hits = 1, window_start = 1000, now = 1000.25 },
            { i = 2, hits = 1, window_start = 960, now = 1000.25 },
        }
        assert.is_nil(run(RPS_RPM))
        assert.are.equal(1, #db.queries)
        local sql = db.queries[1]
        assert.truthy(sql:find("SET LOCAL synchronous_commit = local", 1, true))
        assert.truthy(sql:find("'key-1:second'", 1, true))
        assert.truthy(sql:find("'key-1:minute'", 1, true))
        -- The minute window is only counted when the second window allowed it.
        assert.truthy(sql:find("FROM w1 WHERE w1.hits <= 2", 1, true))
        assert.are.equal(120, db.timeout)
        assert.are.equal("kong_admin", db.config.user)
        assert.are.equal(1, db.pooled)
        assert.is_nil(next(ngx.shared.kong_rate_limiting_counters.data))
    end)

    it("rejects on the shared count with the seconds left in its window", function()
        db.rows = { { i = 1, hits = 3, window_start = 1000, now = 1000.25 } }
        assert_rate_limited(run(RPS_RPM))
        assert.are.equal(1, headers["Retry-After"])

        db.rows = {
            { i = 1, hits = 1, window_start = 1000, now = 1000.25 },
            { i = 2, hits = 61, window_start = 960, now = 1000.25 },
        }
        assert_rate_limited(run(RPS_RPM))
        assert.are.equal(20, headers["Retry-After"])
    end)

    it("falls back to the shared dict and opens the breaker when the database is down", function()
        db.connect_err = "timeout"
        assert.is_nil(run(RPS_RPM))
        assert.are.equal(1, db.connects)
        assert.are.equal(1, #warnings)
        assert.are.equal(1, ngx.shared.kong_rate_limiting_counters.data["neutree_rl:key-1:second:1000"])

        -- Breaker open: no connection attempt, per-pod limits still hold.
        assert.is_nil(run(RPS_RPM))
        assert_rate_limited(run(RPS_RPM))
        assert.are.equal(1, db.connects)

        -- After the cooldown the database is tried again.
        clock = 1005.3
        db.connect_err = nil
        db.rows = { { i = 1, hits = 1, window_start = 1005, now = 1005.3 }, { i = 2, hits = 4, window_start = 960, now = 1005.3 } }
        assert.is_nil(run(RPS_RPM))
        assert.are.equal(2, db.connects)
    end)

    it("does not return a connection to the pool after a failed statement", function()
        db.query_err = "cannot execute INSERT in a read-only transaction"
        assert.is_nil(run(RPS_RPM))
        assert.are.equal(1, db.closed)
        assert.are.equal(0, db.pooled)
        assert.are.equal(1, ngx.shared.kong_rate_limiting_counters.data["neutree_rl:key-1:second:1000"])
    end)

    it("counts per pod when a window is not one the database holds", function()
        assert.is_nil(run({ { limit = 1, window = "hour" } }))
        assert_rate_limited(run({ { limit = 1, window = "hour" } }))
        assert.are.equal(0, db.connects)
    end)
    it("sends new requests per pod while a call has been pending past a third of the timeout", function()
        db.rows = { { i = 1, hits = 1, window_start = 1000, now = 1000.25 } }
        local RPS10 = { { limit = 10, window = "second" } }
        local seen
        db.on_query = function()
            -- 10ms in: still healthy, a concurrent request goes to PG in parallel.
            clock = clock + 0.01
            db.on_query = function()
                -- 50ms in, neither call answered: the next request does not wait.
                clock = clock + 0.04
                assert.is_nil(run(RPS10))
                seen = { connects = db.connects, queries = #db.queries }
                clock = clock + 0.07
                db.query_err = "timeout"
            end
            assert.is_nil(run(RPS10))
        end
        assert.is_nil(run(RPS10))
        assert.are.same({ connects = 2, queries = 2 }, seen)
        assert.are.equal(3, ngx.shared.kong_rate_limiting_counters.data["neutree_rl:key-1:second:1000"])
        -- The pending calls failed, so the breaker is open now.
        assert.is_false(global_counter.available())
        assert.are.equal(1, #warnings)
    end)

    it("lets one request per worker probe PG after the cooldown", function()
        db.connect_err = "timeout"
        run(RPS_RPM)
        assert.are.equal(1, #warnings)

        clock = 1005.3
        db.connect_err = nil
        db.rows = { { i = 1, hits = 1, window_start = 1005, now = 1005.3 } }
        local connects_during_probe
        db.on_query = function()
            -- Requests that arrive while the probe is pending count per pod.
            assert.is_nil(run(RPS_RPM))
            assert.is_nil(run(RPS_RPM))
            connects_during_probe = db.connects
        end
        assert.is_nil(run(RPS_RPM))
        assert.are.equal(2, connects_during_probe)
        assert.are.equal(2, ngx.shared.kong_rate_limiting_counters.data["neutree_rl:key-1:second:1005"])

        -- The probe succeeded: requests go to PG again.
        assert.is_nil(run(RPS_RPM))
        assert.are.equal(3, db.connects)
    end)

    it("keeps probing with one request per cooldown while PG stays down", function()
        db.connect_err = "timeout"
        run(RPS_RPM)
        clock = 1005.3
        run(RPS_RPM)
        assert.are.equal(2, db.connects)
        assert.is_false(global_counter.available())
        clock = 1010.4
        run(RPS_RPM)
        assert.are.equal(3, db.connects)
        assert.are.equal(3, #warnings)
    end)
end)
