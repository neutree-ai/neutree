local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)") or "./"
local routing = assert(loadfile(spec_dir .. "../routing.lua"))()

describe("routing metrics", function()
    local metrics, values, samples, increments, old_ngx, old_kong, old_exporter, old_routing
    local conf = {
        route_prefix = "/workspace/default/external-endpoint/model-aggr",
        model_routes = {{ model = "virtual", targets = {
            { upstream = "a", upstream_model = "actual-a" },
            { upstream = "b", upstream_model = "actual-b" },
        }}},
    }
    local exporter_name = "kong.plugins.prometheus.exporter"
    local routing_name = "kong.plugins.neutree-ai-gateway.routing"

    before_each(function()
        values, samples, increments = {}, {}, {}
        old_ngx, old_kong = _G.ngx, _G.kong
        old_exporter, old_routing = package.loaded[exporter_name], package.loaded[routing_name]
        _G.ngx = { shared = { neutree_ai_gateway_inflight = {
            get = function(_, key) return values[key] end,
        } } }
        _G.kong = { response = { exit = function(code) return code end } }
        local registry = {
            LOCAL_STORAGE = true,
            counter = function()
                return { inc = function(_, value, labels)
                    increments[#increments + 1] = { value = value, labels = labels }
                end }
            end,
            gauge = function(_, _, _, _, local_storage)
                assert.is_true(local_storage)
                return {
                    reset = function() samples = {} end,
                    set = function(_, value, labels)
                        samples[#samples + 1] = { value = value, labels = labels }
                    end,
                }
            end,
        }
        package.loaded[exporter_name] = {
            get_prometheus = function() return registry end,
            collect = function() return samples end,
        }
        package.loaded[routing_name] = routing
        metrics = assert(loadfile(spec_dir .. "../metrics.lua"))()
        metrics.init_worker()
        metrics.configure({ conf })
        increments = {}
    end)

    after_each(function()
        _G.ngx, _G.kong = old_ngx, old_kong
        package.loaded[exporter_name], package.loaded[routing_name] = old_exporter, old_routing
    end)

    it("reads shared admission counts on every scrape without modifying them", function()
        local state = { scope = conf.route_prefix, route = conf.model_routes[1] }
        local key = routing.target_key(state, state.route.targets[1])
        values[key] = 7
        metrics.collect()
        assert.are.equal(7, samples[1].value)
        assert.are.equal(0, samples[2].value)
        assert.are.same({ conf.route_prefix, "virtual", "a", "actual-a" }, samples[1].labels)
        assert.are.equal(7, values[key])
        values[key] = 2
        metrics.collect()
        assert.are.equal(2, samples[1].value)
    end)

    it("removes gauges for targets removed by configuration reload", function()
        metrics.collect()
        assert.are.equal(2, #samples)
        metrics.configure(nil)
        metrics.collect()
        assert.are.equal(0, #samples)
    end)

    it("counts selected-target requests, but not legacy or unselected requests", function()
        local state = { route = conf.model_routes[1], current = conf.model_routes[1].targets[2] }
        metrics.log(conf, state)
        metrics.log(conf, { route = state.route })
        metrics.log(conf, { legacy = true, current = {} })
        assert.are.same({{ value = 1, labels = { conf.route_prefix, "virtual", "b", "actual-b" } }}, increments)
    end)

    it("exposes a zero request baseline before the first request", function()
        metrics.configure({ conf })
        assert.are.equal(2, #increments)
        assert.are.equal(0, increments[1].value)
        assert.are.equal(0, increments[2].value)
    end)

    it("fails the scrape when the admission dictionary is unavailable", function()
        ngx.shared.neutree_ai_gateway_inflight = nil
        assert.are.equal(503, metrics.collect())
    end)
end)
