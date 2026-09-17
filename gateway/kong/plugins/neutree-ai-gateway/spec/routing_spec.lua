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
