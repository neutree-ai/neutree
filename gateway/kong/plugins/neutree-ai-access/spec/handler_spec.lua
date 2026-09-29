-- Unit tests for the neutree-ai-access model allowlist.
--
-- The case under test: a key with an allowed_models list must still be able to
-- read the endpoint's model list. GET /v1/models (and its Anthropic alias) names
-- no model, so the allowlist -- which is a statement about which models may be
-- *called* -- must not judge it. Everything else, the retrieve form included,
-- stays enforced.
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

_G.ngx = {
    now = function() return 1000 end,
    -- Deliberately no rate-limiting dict: the counter-based limits fail open
    -- without one, which leaves the allowlist as the only decision in play.
    shared = {},
}

local denied

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
        set_header = function() end,
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
