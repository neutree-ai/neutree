local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)") or "./"
local routing = assert(loadfile(spec_dir .. "../routing.lua"))()

local function shared(values)
    return {
        incr = function(_, key, amount, init)
            local current = values[key]
            if current == nil then current = init end
            current = current + amount
            values[key] = current
            return current
        end,
    }
end

describe("routing capacity errors", function()
    it("distinguishes capacity exhaustion from an unknown model", function()
        local values = {}
        local conf = {
            route_prefix = "/ee",
            model_routes = {{
                model = "chat",
                targets = {{
                    upstream = "a",
                    upstream_model = "chat",
                    max_inflight_requests = 1,
                }},
            }},
        }
        local state = assert(routing.begin(conf, "chat", { shared = shared(values) }))

        assert(routing.next(state))
        local exhausted = assert(routing.begin(conf, "chat", { shared = shared(values) }))
        local target, reason = routing.next(exhausted)
        assert.is_nil(target)
        assert.are.equal("capacity_exhausted", reason)
        routing.finish(state)

        local missing, missing_reason = routing.begin(conf, "missing", { shared = shared(values) })
        assert.is_nil(missing)
        assert.are.equal("model_not_found", missing_reason)
    end)
end)

describe("routing counter failures", function()
    it("preserves the cause and does not fall back to another target", function()
        local calls = 0
        local state = assert(routing.begin({
            model_routes = {{
                model = "chat",
                targets = {
                    { upstream = "a", upstream_model = "chat", priority = 0, max_inflight_requests = 1 },
                    { upstream = "b", upstream_model = "chat", priority = 1 },
                },
            }},
        }, "chat", { shared = {
            incr = function()
                calls = calls + 1
                return nil, "no memory"
            end,
        } }))
        local target, reason, detail = routing.next(state)
        assert.is_nil(target)
        assert.are.equal("counter_unavailable", reason)
        assert.are.equal("no memory", detail)
        assert.are.equal(1, calls)
        assert.is_nil(state.current)
        assert.is_nil(state.lease)
    end)
end)

describe("routing scheduling", function()
    local original_random

    before_each(function()
        original_random = math.random
    end)

    after_each(function()
        math.random = original_random
    end)

    it("prefers lower priority numbers regardless of weight and target order", function()
        math.random = function() return 0.9999 end
        local state = assert(routing.begin({
            model_routes = {{
                model = "chat",
                targets = {
                    { upstream = "b", upstream_model = "chat", priority = 1, weight = 100 },
                    { upstream = "a", upstream_model = "chat", priority = 0, weight = 1 },
                },
            }},
        }, "chat"))
        assert.are.equal("a", assert(routing.next(state)).upstream)
        routing.finish(state)
    end)

    it("falls back to the next priority when the primary is at capacity", function()
        local conf = {
            model_routes = {{
                model = "chat",
                targets = {
                    { upstream = "a", upstream_model = "chat", priority = 0, max_inflight_requests = 1 },
                    { upstream = "b", upstream_model = "chat", priority = 1 },
                },
            }},
        }
        local env = { shared = shared({}) }
        local first = assert(routing.begin(conf, "chat", env))
        local second = assert(routing.begin(conf, "chat", env))
        assert.are.equal("a", assert(routing.next(first)).upstream)
        assert.are.equal("b", assert(routing.next(second)).upstream)
        routing.finish(first)
        routing.finish(second)
    end)

    for _, sample in ipairs({
        { point = 0, expected = "a" },
        { point = 0.3999, expected = "a" },
        { point = 0.4, expected = "b" },
        { point = 0.9999, expected = "b" },
    }) do
        it("selects the correct weighted interval at random value " .. sample.point, function()
            math.random = function() return sample.point end
            local state = assert(routing.begin({
                model_routes = {{
                    model = "chat",
                    targets = {
                        { upstream = "a", upstream_model = "chat", priority = 0, weight = 2 },
                        { upstream = "b", upstream_model = "chat", priority = 0, weight = 3 },
                    },
                }},
            }, "chat"))
            assert.are.equal(sample.expected, assert(routing.next(state)).upstream)
            routing.finish(state)
        end)
    end
end)

describe("routing lease release", function()
    local conf, env

    before_each(function()
        conf = {
            model_routes = {{
                model = "chat",
                targets = {{
                    upstream = "a",
                    upstream_model = "chat",
                    max_inflight_requests = 1,
                }},
            }},
        }
        env = { shared = shared({}) }
    end)

    it("accepts a new request after the occupied capacity is released", function()
        local first = assert(routing.begin(conf, "chat", env))
        assert(routing.next(first))

        local blocked = assert(routing.begin(conf, "chat", env))
        local target, reason = routing.next(blocked)
        assert.is_nil(target)
        assert.are.equal("capacity_exhausted", reason)

        routing.finish(first)
        local next_request = assert(routing.begin(conf, "chat", env))
        assert.are.equal("a", assert(routing.next(next_request)).upstream)
        routing.finish(next_request)
    end)

    it("releases each request only once even when finish is called twice", function()
        local first = assert(routing.begin(conf, "chat", env))
        assert(routing.next(first))
        routing.finish(first)
        routing.finish(first)

        local second = assert(routing.begin(conf, "chat", env))
        assert.are.equal("a", assert(routing.next(second)).upstream)

        local third = assert(routing.begin(conf, "chat", env))
        local target, reason = routing.next(third)
        assert.is_nil(target)
        assert.are.equal("capacity_exhausted", reason)
        routing.finish(second)
    end)
end)
