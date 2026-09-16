local routing = require("kong.plugins.neutree-ai-gateway.routing")

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
