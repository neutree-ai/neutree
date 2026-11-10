-- Unit tests for the neutree-ai-quota decision.
--
-- The control plane folds every limit on a key into one remaining count
-- (api.get_api_key_remaining): the key's own quota and every token pool that
-- applies to its owner -- a group's, the owner's own, or one set for the owner
-- in some Team or OrgUnit. The plugin only enforces that number, so these tests
-- pin what it does with each answer, and that it asks per request dimensions.
--
-- Run: make gateway-lua-test (the runner names this spec directory)

local cjson = require("cjson.safe")

local request = {}
local denied
local calls

-- One fake control plane answer per test: { status = ..., body = ... } or
-- { err = ... } for a transport failure.
local answer

package.loaded["resty.http"] = {
    new = function()
        return {
            set_timeout = function() end,
            request_uri = function(_, url, opts)
                calls[#calls + 1] = { url = url, body = cjson.decode(opts.body), headers = opts.headers }
                if answer.err then
                    return nil, answer.err
                end
                return { status = answer.status or 200, body = answer.body }
            end,
        }
    end,
}

-- kong.cache memoizes per key; the stub keeps that so the cache key is tested.
local cache_store

_G.kong = {
    request = {
        get_raw_body = function() return request.body end,
    },
    client = {
        get_consumer = function() return request.consumer end,
    },
    ctx = { shared = {} },
    cache = {
        get = function(_, key, _, fn, ...)
            if cache_store[key] == nil then
                cache_store[key] = fn(...)
            end
            return cache_store[key]
        end,
    },
    response = {
        exit = function(status, body)
            denied = { status = status, body = body }
        end,
    },
    log = { warn = function() end, err = function() end },
}

local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)") or "./"
local handler = assert(loadfile(spec_dir .. "../handler.lua"))()

local conf = { api_url = "http://api", service_token = "t", cache_ttl = 5, timeout = 100 }

local function run(model)
    request.consumer = { custom_id = "key-1" }
    request.body = nil
    kong.ctx.shared = { neutree_request_model = model }
    handler:access(conf)
end

describe("neutree-ai-quota", function()
    before_each(function()
        denied = nil
        calls = {}
        cache_store = {}
        answer = { status = 200, body = "null" }
    end)

    it("rejects with 429 when the remaining count is used up", function()
        answer.body = "0"
        run("m")
        assert.are.equal(429, denied.status)
        assert.are.equal("quota_exceeded", denied.body.error.code)
    end)

    it("rejects an overdrawn pool too", function()
        -- Pools are soft: usage arrives after aggregation, so remaining can be
        -- negative once one is overdrawn.
        answer.body = "-10"
        run("m")
        assert.are.equal(429, denied.status)
    end)

    it("lets a request through while something remains", function()
        answer.body = "1"
        run("m")
        assert.is_nil(denied)
    end)

    it("treats null as no limit", function()
        run("m")
        assert.is_nil(denied)
    end)

    it("fails open when the control plane cannot answer", function()
        answer = { err = "timeout" }
        run("m")
        assert.is_nil(denied)

        cache_store = {}
        answer = { status = 500, body = "{}" }
        run("m")
        assert.is_nil(denied)
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
end)
