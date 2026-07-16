-- Unit tests for the neutree-ai-gateway plugin's pure helpers, focused on the
-- JSON array/object handling policy (NEU-551).
--
-- These tests run under plain LuaJIT/Lua 5.1 with the luarocks `lua-cjson`
-- (the OpenResty fork, which ships array_mt / decode_array_with_array_mt /
-- empty_array) and `busted`. The handler pulls in a few OpenResty- and
-- Kong-only modules; we stub them via package.loaded/globals BEFORE requiring
-- it, since the helpers under test never touch them at call time.
--
-- Run: make gateway-lua-test   (or, with deps installed: busted spec/)

-- Stub OpenResty / Kong dependencies so the handler loads outside Kong. Must be
-- registered before the handler is required.
package.loaded["string.buffer"] = {
    new = function()
        return {
            put = function() end,
            get = function() return "" end,
            tostring = function() return "" end,
        }
    end,
}
package.loaded["kong.llm.drivers.shared"] = {
    calculate_cost = function() return 0 end,
    frame_to_events = function() return {} end,
    from_format = function() return nil end,
    _CONST = { SSE_TERMINATOR = "[DONE]" },
}
package.loaded["kong.llm.drivers.openai"] = {
    from_format = function() return nil end,
}
package.loaded["kong.tools.string"] = {
    strip = function(s) return s end,
}

_G.ngx = { now = function() return 0 end }
_G.kong = { log = { warn = function() end, err = function() end } }

-- Load the handler by file path (relative to this spec) rather than by module
-- name, so the tests do not depend on the kong.plugins.* directory nesting
-- being present — the plugin dir is often bind-mounted on its own.
local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)") or "./"

local cjson = require("cjson.safe")
local handler = assert(loadfile(spec_dir .. "../handler.lua"))()
local T = handler._TEST

-- plain-substring find helpers (cjson emits no spaces, and a key and its value
-- are always contiguous, so `"required":[]` is a reliable literal probe).
local function has(str, needle)
    return str:find(needle, 1, true) ~= nil
end

describe("json_array()", function()
    it("encodes an empty list as [] rather than {}", function()
        assert.are.equal("[]", cjson.encode(T.json_array()))
    end)

    it("wraps and encodes a populated list as an array", function()
        local a = T.json_array()
        a[#a + 1] = { id = "x" }
        assert.are.equal('[{"id":"x"}]', cjson.encode(a))
    end)
end)

describe("cjson array/object round-trip policy", function()
    -- The handler enables decode_array_with_array_mt(true) at module load; this
    -- is the mechanism access() relies on when it decodes and re-encodes the
    -- client body on the model-mapping paths.
    it("preserves empty arrays and empty objects distinctly", function()
        local body = [[{"required":[],"enum":[],"properties":{}}]]
        local out = cjson.encode(cjson.decode(body))
        assert.is_true(has(out, '"required":[]'))
        assert.is_true(has(out, '"enum":[]'))
        assert.is_true(has(out, '"properties":{}'))
        assert.is_false(has(out, '"required":{}'))
    end)

    it("simulates the OpenAI passthrough re-encode of a tool schema", function()
        -- goose sends fully-optional tools with required:[]; access() decodes,
        -- rewrites model, and re-encodes. The empty array must survive.
        local body = [[{"model":"deepseek-v4-pro[1m]","tools":[{"type":"function",]]
            .. [["function":{"name":"create_browser","parameters":]]
            .. [[{"type":"object","properties":{},"required":[]}}}]}]]
        local req = cjson.decode(body)
        req.model = "deepseek-v4-pro" -- the only mutation access() makes here
        local out = cjson.encode(req)
        assert.is_true(has(out, '"required":[]'))
        assert.is_true(has(out, '"properties":{}'))
        assert.is_false(has(out, '"required":{}'))
    end)
end)

describe("convert_tools()", function()
    it("returns nil for nil, non-table, and empty input", function()
        assert.is_nil(T.convert_tools(nil))
        assert.is_nil(T.convert_tools("nope"))
        assert.is_nil(T.convert_tools({}))
    end)

    it("maps anthropic tools to OpenAI function tools", function()
        local tools = T.convert_tools({
            { name = "t", description = "d", input_schema = { type = "object" } },
        })
        assert.are.equal(1, #tools)
        assert.are.equal("function", tools[1].type)
        assert.are.equal("t", tools[1]["function"].name)
        assert.are.equal("d", tools[1]["function"].description)
        assert.are.same({ type = "object" }, tools[1]["function"].parameters)
    end)
end)

describe("convert_request()", function()
    it("keeps an empty required array through the anthropic->openai path", function()
        local anthropic = cjson.decode([[{"model":"deepseek","max_tokens":16,]]
            .. [["messages":[{"role":"user","content":"hi"}],]]
            .. [["tools":[{"name":"create_browser","input_schema":]]
            .. [[{"type":"object","properties":{},"required":[]}}]}]])
        local openai = T.convert_request(anthropic)
        local enc = cjson.encode(openai)
        assert.is_true(has(enc, '"required":[]'))
        assert.is_true(has(enc, '"properties":{}'))
        assert.is_false(has(enc, '"required":{}'))
    end)

    it("omits an empty stop_sequences list", function()
        local openai = T.convert_request(cjson.decode(
            [[{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"stop_sequences":[]}]]))
        assert.is_nil(openai.stop)
    end)

    it("forwards a non-empty stop_sequences list as stop", function()
        local openai = T.convert_request(cjson.decode(
            [[{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"stop_sequences":["X"]}]]))
        assert.are.same({ "X" }, openai.stop)
    end)
end)

describe("convert_response()", function()
    it("emits content as a non-empty array even when the model returns nothing", function()
        local resp = T.convert_response({ choices = { { message = {} } } }, "m")
        -- content must serialise as a JSON array, never {}
        assert.is_true(cjson.encode(resp.content):sub(1, 1) == "[")
        assert.are.equal("text", resp.content[1].type)
        assert.are.equal("", resp.content[1].text)
    end)

    it("represents tool_use.input as a JSON object", function()
        local resp = T.convert_response({
            choices = { { message = { tool_calls = {
                { id = "c1", ["function"] = { name = "f" } },
            } } } },
        }, "m")
        local tool_use = resp.content[#resp.content]
        assert.are.equal("tool_use", tool_use.type)
        assert.are.equal("{}", cjson.encode(tool_use.input))
    end)
end)

describe("make_message_start()", function()
    it("carries content as an empty JSON array, not {}", function()
        local ev = T.make_message_start("m", 5)
        assert.are.equal("[]", cjson.encode(ev.message.content))
    end)
end)
