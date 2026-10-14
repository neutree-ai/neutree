-- Unit tests for the neutree-ai-access model allowlist.
--
-- A model-list request names no one model, so it must reach the endpoint instead
-- of receiving a model_not_permitted error in access(). Its collection response
-- is then filtered with the same endpoint-scoped allowlist that inference uses.
-- The retrieve form still names one model and stays enforced in access().
--
-- The handler is exercised through its public access() entry point rather than
-- through exported helpers, so the tests pin the plugin's decision for a request
-- shape, not the shape of its internals. The Kong PDK surface the handler touches
-- is stubbed below; the stubs also record any denial so a test can assert on it.
--
-- These tests run under LuaJIT/Lua 5.1 with the luarocks `lua-cjson` and `busted`.
-- Run: make gateway-lua-test (the runner names this spec directory)

-- Plain Lua can't require the real PDK, so the surface the handler uses is
-- installed as globals before the handler is loaded.
local request = {}
local cjson = require("cjson.safe")

_G.ngx = {
    now = function() return 1000 end,
    -- Deliberately no rate-limiting dict: the counter-based limits fail open
    -- without one, which leaves the allowlist as the only decision in play.
    shared = {},
    status = 200,
    arg = {},
}

local denied
local buffered_response
local cleared_headers
local response_headers

_G.kong = {
    request = {
        get_method = function() return request.method end,
        get_path = function() return request.path end,
        get_raw_body = function() return request.body end,
    },
    client = {
        get_consumer = function() return request.consumer end,
    },
    ctx = { shared = {}, plugin = {} },
    response = {
        exit = function(status, body)
            denied = { status = status, body = body }
        end,
        set_header = function(name, value) response_headers[name] = value end,
        get_status = function() return ngx.status end,
        set_status = function(status) ngx.status = status end,
        clear_header = function(name) cleared_headers[name] = true end,
        get_raw_body = function()
            buffered_response = (buffered_response or "") .. (ngx.arg[1] or "")
            if not ngx.arg[2] then
                ngx.arg[1] = nil
                return nil
            end
            return buffered_response
        end,
        set_raw_body = function(body) ngx.arg[1] = body end,
    },
    log = { warn = function() end, err = function() end },
}

-- Load the handler by file path (relative to this spec) rather than by module
-- name, so the tests do not depend on the kong.plugins.* directory nesting being
-- present -- the plugin dir is often bind-mounted on its own.
local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)") or "./"
local handler = assert(loadfile(spec_dir .. "../handler.lua"))()

local WORKSPACE_PREFIX = "/workspace/ws/endpoint/ep-a"

-- conf_with builds the plugin config the control plane reconciles onto a key
-- (see internal/gateway/kong.go generateAPIKeyAccessPlugin). nil entries means
-- the key has no allowlist, i.e. unrestricted.
local function conf_with(entries, overrides)
    local conf = {
        disabled = false,
        allowed_models = entries,
        concurrency = 0,
        rate_limits = {},
    }
    for k, v in pairs(overrides or {}) do
        conf[k] = v
    end
    return conf
end

-- run drives one request through the plugin and returns the denial it produced
-- (nil when the request was allowed through).
--
-- fields:
--   method, path, body -- the request as Kong sees it in the access phase;
--   model  -- the client-facing model neutree-ai-gateway stashes before it
--             rewrites the body (absent when that plugin skipped the request);
--   endpoint_name -- the IE/EE identity that plugin stashes per route.
local function run(conf, fields)
    denied = nil
    request.method = fields.method or "POST"
    request.path = fields.path
    request.body = fields.body
    request.consumer = { custom_id = "key-1" }

    kong.ctx.plugin = {}
    kong.ctx.shared = {
        neutree_endpoint_type = fields.endpoint_type or "internal",
        neutree_endpoint_name = fields.endpoint_name or "ep-a",
        neutree_request_model = fields.model,
    }

    handler:access(conf)
    return denied
end

local function assert_denied(res, code)
    assert.is_not_nil(res, "expected the request to be denied")
    assert.are.equal(403, res.status)
    assert.are.equal(code, res.body.error.code)
end

-- filter_response drives one model-list response through body_filter(). The
-- response may arrive in chunks, as it does when Kong proxies an internal
-- endpoint. An external endpoint's synthetic response is simply one chunk.
local function filter_response(conf, fields, chunks)
    request.method = fields.method or "GET"
    request.path = fields.path
    request.body = nil
    request.consumer = { custom_id = "key-1" }

    kong.ctx.plugin = {}
    kong.ctx.shared = {
        neutree_endpoint_type = fields.endpoint_type or "internal",
        neutree_endpoint_name = fields.endpoint_name or "ep-a",
    }

    ngx.status = fields.status or 200
    buffered_response = nil
    cleared_headers = {}
    response_headers = {}
    handler:header_filter(conf)
    local response = {}
    for i, chunk in ipairs(chunks) do
        ngx.arg[1] = chunk
        ngx.arg[2] = i == #chunks
        handler:body_filter(conf)
        response[#response + 1] = ngx.arg[1] or ""
    end

    return table.concat(response), cleared_headers, ngx.status, response_headers
end

local CHAT_ALLOWLIST = conf_with({
    { model = "llama-3-8b", type = "internal", endpoint_name = "ep-a" },
})

describe("neutree-ai-access model allowlist: model-discovery requests", function()
    it("allows GET /v1/models for a key whose allowlist pins the endpoint", function()
        assert.is_nil(run(CHAT_ALLOWLIST, { method = "GET", path = WORKSPACE_PREFIX .. "/v1/models" }))
    end)

    it("allows a trailing slash on the model-list path", function()
        assert.is_nil(run(CHAT_ALLOWLIST, { method = "GET", path = WORKSPACE_PREFIX .. "/v1/models/" }))
    end)

    it("allows the Anthropic model-list alias", function()
        assert.is_nil(run(CHAT_ALLOWLIST, { method = "GET", path = WORKSPACE_PREFIX .. "/anthropic/v1/models" }))
    end)

    it("allows the model list on an endpoint whose route is not chat completions", function()
        local conf = conf_with({
            { model = "bge-m3", type = "internal", endpoint_name = "ep-emb" },
        })
        assert.is_nil(run(conf, {
            method = "GET",
            path = "/workspace/ws/endpoint/ep-emb/v1/models",
            endpoint_name = "ep-emb",
        }))
    end)

    it("allows the model list for a deny-all key", function()
        assert.is_nil(run(conf_with({}), { method = "GET", path = WORKSPACE_PREFIX .. "/v1/models" }))
    end)

    it("allows the model list for a key without an allowlist", function()
        assert.is_nil(run(conf_with(nil), { method = "GET", path = WORKSPACE_PREFIX .. "/v1/models" }))
    end)

    it("denies a non-GET on the model-list path", function()
        assert_denied(run(CHAT_ALLOWLIST, { method = "POST", path = WORKSPACE_PREFIX .. "/v1/models" }),
            "model_not_permitted")
    end)

    it("denies paths that merely resemble the model list", function()
        for _, path in ipairs({ "/v1/models-x", "/v1/modelsx", "/v1/modelsx/y" }) do
            assert_denied(run(CHAT_ALLOWLIST, { method = "GET", path = WORKSPACE_PREFIX .. path }),
                "model_not_permitted")
        end
    end)

    it("still denies the retrieve form, which names a model", function()
        for _, id in ipairs({ "llama-3-8b", "Qwen/Qwen3-8B" }) do
            assert_denied(run(CHAT_ALLOWLIST, { method = "GET", path = WORKSPACE_PREFIX .. "/v1/models/" .. id }),
                "model_not_permitted")
        end
    end)

    it("still denies a disabled key", function()
        local conf = conf_with({ { model = "llama-3-8b" } }, { disabled = true })
        assert_denied(run(conf, { method = "GET", path = WORKSPACE_PREFIX .. "/v1/models" }), "key_disabled")
    end)
end)

describe("neutree-ai-access model allowlist: inference requests", function()
    it("allows an allowed model", function()
        assert.is_nil(run(CHAT_ALLOWLIST, {
            method = "POST",
            path = WORKSPACE_PREFIX .. "/v1/chat/completions",
            model = "llama-3-8b",
            body = '{"model":"llama-3-8b"}',
        }))
    end)

    it("denies a model outside the allowlist", function()
        assert_denied(run(CHAT_ALLOWLIST, {
            method = "POST",
            path = WORKSPACE_PREFIX .. "/v1/chat/completions",
            model = "gpt-4",
            body = '{"model":"gpt-4"}',
        }), "model_not_permitted")
    end)

    it("denies an allowed model on an endpoint the entry does not pin", function()
        assert_denied(run(CHAT_ALLOWLIST, {
            method = "POST",
            path = "/workspace/ws/endpoint/ep-b/v1/chat/completions",
            endpoint_name = "ep-b",
            body = '{"model":"llama-3-8b"}',
        }), "model_not_permitted")
    end)

    it("denies a request that names no model", function()
        assert_denied(run(CHAT_ALLOWLIST, {
            method = "POST",
            path = WORKSPACE_PREFIX .. "/v1/chat/completions",
            body = "{}",
        }), "model_not_permitted")
    end)

    it("reads the model from the body when no model was stashed", function()
        assert.is_nil(run(CHAT_ALLOWLIST, {
            method = "POST",
            path = "/workspace/ws/endpoint/ep-a/v1/completions",
            body = '{"model":"llama-3-8b"}',
        }))
    end)
end)

describe("neutree-ai-access model allowlist: model-list responses", function()
    for _, protocol in ipairs({
        {
            name = "OpenAI",
            path = "/workspace/ws/external-endpoint/provider-a/v1/models",
            body = [[{"object":"list","data":[{"id":"visible-before-disable"}]}]],
        },
        {
            name = "Anthropic",
            path = "/workspace/ws/external-endpoint/provider-a/anthropic/v1/models",
            body = [[{"data":[{"id":"visible-before-disable"}],"has_more":false,"first_id":"visible-before-disable","last_id":"visible-before-disable"}]],
        },
    }) do
        it("returns key_disabled for a disabled external " .. protocol.name .. " list", function()
            local body, headers, status, response_headers = filter_response(conf_with(nil, { disabled = true }), {
                path = protocol.path,
                endpoint_type = "external",
                endpoint_name = "provider-a",
            }, { protocol.body })

            local decoded = assert(cjson.decode(body))
            assert.are.equal(403, status)
            assert.are.equal(true, headers["Content-Length"])
            assert.are.equal("application/json", response_headers["Content-Type"])
            assert.are.equal("key_disabled", decoded.error.code)
            assert.are.equal("This API key is disabled", decoded.error.message)
        end)
    end

    it("lets disabled override an external model allowlist response filter", function()
        local body, _, status = filter_response(conf_with({ {
            model = "visible-before-disable",
            type = "external",
            endpoint_name = "provider-a",
        } }, { disabled = true }), {
            path = "/workspace/ws/external-endpoint/provider-a/v1/models",
            endpoint_type = "external",
            endpoint_name = "provider-a",
        }, { [[{"object":"list","data":[{"id":"visible-before-disable"}]}]] })

        local decoded = assert(cjson.decode(body))
        assert.are.equal(403, status)
        assert.are.equal("key_disabled", decoded.error.code)
    end)

    it("filters a chunked internal OpenAI list by model, type, and endpoint", function()
        local conf = conf_with({
            { model = "allowed", type = "internal", endpoint_name = "ep-a" },
            { model = "same-name-wrong-source", type = "external", endpoint_name = "ep-a" },
            { model = "same-name-wrong-endpoint", type = "internal", endpoint_name = "ep-b" },
            { model = "legacy-name-only" },
        })
        local original = [[{"object":"list","data":[{"id":"allowed"},{"id":"same-name-wrong-source"},{"id":"same-name-wrong-endpoint"},{"id":"legacy-name-only"}]}]]
        local body, headers = filter_response(conf, {
            path = WORKSPACE_PREFIX .. "/v1/models",
            endpoint_type = "internal",
            endpoint_name = "ep-a",
        }, { original:sub(1, 51), original:sub(52) })

        local decoded = assert(cjson.decode(body))
        assert.are.equal(true, headers["Content-Length"])
        assert.are.equal("list", decoded.object)
        assert.are.equal(2, #decoded.data)
        assert.are.same({ "allowed", "legacy-name-only" }, {
            decoded.data[1].id,
            decoded.data[2].id,
        })
    end)

    it("filters an external Anthropic list and keeps its collection envelope", function()
        local conf = conf_with({
            { model = "allowed", type = "external", endpoint_name = "provider-a" },
            { model = "internal-only", type = "internal", endpoint_name = "provider-a" },
        })
        local body = filter_response(conf, {
            path = "/workspace/ws/external-endpoint/provider-a/anthropic/v1/models",
            endpoint_type = "external",
            endpoint_name = "provider-a",
        }, { [[{"data":[{"id":"allowed","type":"model"},{"id":"internal-only","type":"model"}],"has_more":false,"first_id":"allowed","last_id":"internal-only"}]] })

        local decoded = assert(cjson.decode(body))
        assert.are.equal(false, decoded.has_more)
        assert.are.equal("allowed", decoded.first_id)
        assert.are.equal("allowed", decoded.last_id)
        assert.are.equal(1, #decoded.data)
        assert.are.same({ "allowed" }, { decoded.data[1].id })
    end)

    it("filters an internal Anthropic list with the same endpoint scope", function()
        local conf = conf_with({
            { model = "allowed", type = "internal", endpoint_name = "ep-a" },
            { model = "other-endpoint", type = "internal", endpoint_name = "ep-b" },
        })
        local body = filter_response(conf, {
            path = WORKSPACE_PREFIX .. "/anthropic/v1/models",
            endpoint_type = "internal",
            endpoint_name = "ep-a",
        }, { [[{"data":[{"id":"allowed"},{"id":"other-endpoint"}],"has_more":false,"first_id":"allowed","last_id":"other-endpoint"}]] })

        local decoded = assert(cjson.decode(body))
        assert.are.equal("allowed", decoded.first_id)
        assert.are.equal("allowed", decoded.last_id)
        assert.are.equal(1, #decoded.data)
        assert.are.same({ "allowed" }, { decoded.data[1].id })
    end)

    it("filters an external OpenAI list with the same endpoint scope", function()
        local conf = conf_with({
            { model = "allowed", type = "external", endpoint_name = "provider-a" },
            { model = "internal-only", type = "internal", endpoint_name = "provider-a" },
        })
        local body = filter_response(conf, {
            path = "/workspace/ws/external-endpoint/provider-a/v1/models",
            endpoint_type = "external",
            endpoint_name = "provider-a",
        }, { [[{"object":"list","data":[{"id":"allowed"},{"id":"internal-only"}]}]] })

        local decoded = assert(cjson.decode(body))
        assert.are.equal("list", decoded.object)
        assert.are.equal(1, #decoded.data)
        assert.are.same({ "allowed" }, { decoded.data[1].id })
    end)

    it("returns an HTTP-200-compatible empty array for an explicit deny-all list", function()
        local body = filter_response(conf_with({}), {
            path = WORKSPACE_PREFIX .. "/v1/models",
        }, { [[{"object":"list","data":[{"id":"visible-without-filter"}]}]] })

        local decoded = assert(cjson.decode(body))
        assert.are.equal("list", decoded.object)
        assert.are.equal(0, #decoded.data)
        assert.is_truthy(body:find('"data":[]', 1, true))
    end)

    it("leaves a model list unchanged when allowed_models is not configured", function()
        local original = [[{"object":"list","data":[{"id":"first"},{"id":"second"}]}]]
        assert.are.equal(original, filter_response(conf_with(nil), {
            path = WORKSPACE_PREFIX .. "/v1/models",
        }, { original }))
    end)

    it("leaves a model list unchanged when allowed_models is JSON null", function()
        local original = [[{"object":"list","data":[{"id":"first"},{"id":"second"}]}]]
        assert.are.equal(original, filter_response(conf_with(cjson.null), {
            path = WORKSPACE_PREFIX .. "/v1/models",
        }, { original }))
    end)

    it("does not filter a model-detail response", function()
        local original = [[{"id":"not-permitted","object":"model"}]]
        assert.are.equal(original, filter_response(CHAT_ALLOWLIST, {
            path = WORKSPACE_PREFIX .. "/v1/models/not-permitted",
        }, { original }))
    end)

    it("does not alter a non-success collection response", function()
        local original = [[{"error":{"message":"upstream unavailable"}}]]
        assert.are.equal(original, filter_response(CHAT_ALLOWLIST, {
            path = WORKSPACE_PREFIX .. "/v1/models",
            status = 503,
        }, { original }))
    end)

    it("does not replace a non-success model list for a disabled key", function()
        local original = [[{"error":{"message":"gateway unavailable"}}]]
        assert.are.equal(original, filter_response(conf_with(nil, { disabled = true }), {
            path = "/workspace/ws/external-endpoint/provider-a/v1/models",
            endpoint_type = "external",
            endpoint_name = "provider-a",
            status = 503,
        }, { original }))
    end)

    it("passes through an invalid successful body unchanged", function()
        local original = "not a JSON model list"
        assert.are.equal(original, filter_response(CHAT_ALLOWLIST, {
            path = WORKSPACE_PREFIX .. "/v1/models",
        }, { original }))
    end)
end)
