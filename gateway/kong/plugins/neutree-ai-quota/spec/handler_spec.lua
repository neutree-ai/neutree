-- Unit tests for the neutree-ai-quota decision.
--
-- The control plane folds every limit on a key into one remaining count
-- (api.get_api_key_remaining): the key's own quota and every token pool that
-- applies to its owner -- a group's, the owner's own, or one set for the owner
-- in some Team or OrgUnit. The plugin only enforces that number, so these tests
-- pin what it does with each answer, that it asks per request dimensions, and
-- how its stale-while-revalidate cache behaves when the answer is old or the
-- control plane is down.
--
-- Run: make gateway-lua-test (the runner names this spec directory)

local cjson = require("cjson.safe")

local request = {}
local denied
local calls

-- One fake control plane answer at a time: { status = ..., body = ... } or
-- { err = ... } for a transport failure. While `hold` is set, a fetch yields
-- (the coroutine stands in for a request parked on a slow socket).
local answer
local hold
local timeouts

package.loaded["resty.http"] = {
    new = function()
        return {
            set_timeout = function(_, ms) timeouts[#timeouts + 1] = ms end,
            request_uri = function(_, url, opts)
                calls[#calls + 1] = { url = url, body = cjson.decode(opts.body), headers = opts.headers }
                while hold do
                    coroutine.yield()
                end
                if answer.err then
                    return nil, answer.err
                end
                return { status = answer.status or 200, body = answer.body }
            end,
        }
    end,
}

package.loaded["resty.lrucache"] = {
    new = function()
        local t = {}
        return {
            get = function(_, k) return t[k] end,
            set = function(_, k, v) t[k] = v end,
        }
    end,
}

-- Cooperative semaphore: wait yields until a post hands out a resource.
package.loaded["ngx.semaphore"] = {
    new = function()
        local s = { resources = 0 }
        function s:post(n) self.resources = self.resources + (n or 1) end
        function s:wait()
            while self.resources == 0 do
                coroutine.yield()
            end
            self.resources = self.resources - 1
            return true
        end
        return s
    end,
}

local clock
local timers
local timer_fails

_G.ngx = {
    now = function() return clock end,
    log = function() end,
    WARN = "warn",
    timer = {
        at = function(_, fn, ...)
            if timer_fails then
                return nil, "too many pending timers"
            end
            timers[#timers + 1] = { fn = fn, args = { ... } }
            return true
        end,
    },
}

-- Timers run outside any request, so the request PDK is unavailable there; the
-- stub enforces that by failing loudly if a timer touches it.
local in_timer = false
local function request_api(fn)
    return function(...)
        assert(not in_timer, "request-scoped PDK API used in a timer")
        return fn(...)
    end
end

_G.kong = {
    request = {
        get_raw_body = request_api(function() return request.body end),
    },
    client = {
        get_consumer = request_api(function() return request.consumer end),
    },
    ctx = { shared = {} },
    response = {
        exit = request_api(function(status, body)
            denied = { status = status, body = body }
        end),
    },
    log = { warn = function() end, err = function() end },
}

local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)") or "./"
local handler

local conf = { api_url = "http://api", service_token = "t", cache_ttl = 5, timeout = 100 }

local function run(model)
    denied = nil
    request.consumer = { custom_id = "key-1" }
    request.body = nil
    kong.ctx.shared = { neutree_request_model = model }
    handler:access(conf)
    -- Read and clear in one step: concurrent coroutines share `denied`.
    local d = denied
    denied = nil
    return d
end

local function run_timers()
    local pending = timers
    timers = {}
    in_timer = true
    for _, t in ipairs(pending) do
        t.fn(false, unpack(t.args))
    end
    in_timer = false
end

describe("neutree-ai-quota", function()
    before_each(function()
        -- Fresh module per test: the cache lives at module level.
        handler = assert(loadfile(spec_dir .. "../handler.lua"))()
        denied = nil
        calls = {}
        timeouts = {}
        clock = 1000
        timers = {}
        timer_fails = false
        hold = false
        answer = { status = 200, body = "null" }
    end)

    it("rejects with 429 when the remaining count is used up", function()
        answer.body = "0"
        local d = run("m")
        assert.are.equal(429, d.status)
        assert.are.equal("quota_exceeded", d.body.error.code)
    end)

    it("rejects an overdrawn pool too", function()
        -- Pools are soft: usage arrives after aggregation, so remaining can be
        -- negative once one is overdrawn.
        answer.body = "-10"
        assert.are.equal(429, run("m").status)
    end)

    it("lets a request through while something remains", function()
        answer.body = "1"
        assert.is_nil(run("m"))
    end)

    it("treats null as no limit", function()
        assert.is_nil(run("m"))
    end)

    it("asks for the key and the request model, one cache entry per model", function()
        answer.body = "5"
        run("a")
        run("a")
        run("b")

        assert.are.equal(2, #calls)
        assert.are.equal("http://api/rpc/get_api_key_remaining", calls[1].url)
        assert.are.equal("key-1", calls[1].body.p_id)
        assert.are.equal("a", calls[1].body.p_model)
        assert.are.equal("b", calls[2].body.p_model)
        assert.are.equal("Bearer t", calls[1].headers.Authorization)
    end)

    it("does nothing for a request without a consumer", function()
        request.consumer = nil
        handler:access(conf)
        assert.are.equal(0, #calls)
    end)

    it("serves a fresh value without fetching or scheduling a refresh", function()
        answer.body = "0"
        run("m")
        clock = clock + 4
        assert.are.equal(429, run("m").status)
        assert.are.equal(1, #calls)
        assert.are.equal(0, #timers)
    end)

    it("decides an expired entry by the old value and refreshes it in the background", function()
        answer.body = "0"
        run("m")

        clock = clock + 5
        answer.body = "100"
        -- Still the old answer, and nothing fetched on the request path.
        assert.are.equal(429, run("m").status)
        assert.are.equal(1, #calls)
        assert.are.equal(1, #timers)

        run_timers()
        assert.are.equal(2, #calls)
        assert.is_nil(run("m"))
    end)

    it("gives a background refresh a longer timeout than the request path", function()
        run("m")
        clock = clock + 5
        run("m")
        run_timers()
        assert.are.same({ conf.timeout, 10000 }, timeouts)
    end)

    it("schedules at most one refresh per entry at a time", function()
        answer.body = "5"
        run("m")
        clock = clock + 10
        run("m")
        run("m")
        run("m")
        assert.are.equal(1, #timers)

        run_timers()
        assert.are.equal(2, #calls)
        -- The refresh just landed, so the entry is fresh again.
        run("m")
        assert.are.equal(0, #timers)
    end)

    it("keeps a rejecting value when the refresh fails", function()
        answer.body = "0"
        run("m")

        for _, a in ipairs({ { err = "timeout" }, { status = 500, body = "{}" }, { status = 200, body = "oops" } }) do
            answer = a
            clock = clock + 5
            assert.are.equal(429, run("m").status)
            run_timers()
            assert.are.equal(429, run("m").status)
        end
        assert.are.equal(4, #calls)
    end)

    it("keeps an allowing value when the refresh fails", function()
        answer.body = "3"
        run("m")
        answer = { err = "timeout" }
        clock = clock + 5
        run("m")
        run_timers()
        assert.is_nil(run("m"))

        -- Once the control plane is back, the new value applies.
        answer = { status = 200, body = "0" }
        clock = clock + 5
        run("m")
        run_timers()
        assert.are.equal(429, run("m").status)
    end)

    it("waits a full cache period before retrying a failed refresh", function()
        answer.body = "0"
        run("m")
        answer = { err = "timeout" }
        clock = clock + 5
        run("m")
        run_timers()

        clock = clock + 1
        run("m")
        assert.are.equal(0, #timers)
    end)

    it("fails open on a first failed fetch, then retries in the background", function()
        answer = { err = "timeout" }
        assert.is_nil(run("m"))
        assert.are.equal(1, #calls)

        -- Within the cache period: allowed without another (slow) fetch.
        assert.is_nil(run("m"))
        assert.are.equal(1, #calls)
        assert.are.equal(0, #timers)

        -- After it: still allowed at once, retried off the request path.
        answer = { status = 200, body = "0" }
        clock = clock + 5
        assert.is_nil(run("m"))
        assert.are.equal(1, #calls)
        run_timers()
        assert.are.equal(2, #calls)
        assert.are.equal(429, run("m").status)
    end)

    it("does not survive a refresh failure as unlimited after a first failure", function()
        answer = { status = 500, body = "{}" }
        run("m")
        clock = clock + 5
        run("m")
        run_timers()
        clock = clock + 5
        answer = { status = 200, body = "0" }
        run("m")
        run_timers()
        assert.are.equal(429, run("m").status)
    end)

    it("lets concurrent first requests share one fetch", function()
        answer.body = "0"
        hold = true
        local results = {}
        local cos = {}
        for i = 1, 3 do
            cos[i] = coroutine.create(function() results[i] = run("m") end)
            assert(coroutine.resume(cos[i]))
        end
        hold = false
        local alive = true
        while alive do
            alive = false
            for _, co in ipairs(cos) do
                if coroutine.status(co) ~= "dead" then
                    assert(coroutine.resume(co))
                    alive = true
                end
            end
        end

        assert.are.equal(1, #calls)
        for i = 1, 3 do
            assert.are.equal(429, results[i].status)
        end
    end)

    it("schedules again later if a refresh timer cannot be created", function()
        answer.body = "0"
        run("m")
        clock = clock + 5
        timer_fails = true
        assert.are.equal(429, run("m").status)
        timer_fails = false
        run("m")
        assert.are.equal(1, #timers)
    end)
end)
