local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)")
local routing = assert(loadfile(spec_dir .. "../routing.lua"))()

describe("routing observation contract", function()
    local metrics, values, series, conf, ctx, enabled, increments
    before_each(function()
        values, series, enabled, increments = {}, {}, true, 0
        local registry = { LOCAL_STORAGE = true }
        local function metric(_, name)
            series[name] = series[name] or {}
            local data = series[name]
            return {
                inc = function(_, value, labels)
                    increments = increments + 1
                    local key = table.concat(labels, "|")
                    data[key] = (data[key] or 0) + value
                end,
                observe = function(_, value, labels) data[table.concat(labels, "|")] = value end,
                set = function(_, value, labels) data[table.concat(labels, "|")] = value end,
                reset = function() for key in pairs(data) do data[key] = nil end end,
            }
        end
        registry.counter, registry.histogram, registry.gauge = metric, metric, metric
        package.loaded["kong.plugins.prometheus.exporter"] = {
            get_prometheus = function() return enabled and registry or nil end,
            collect = function() return 200 end,
        }
        package.loaded["kong.plugins.neutree-ai-gateway.routing"] = routing
        _G.ngx = { var = { request_time = "1.25" }, shared = { neutree_ai_gateway_inflight = {
            get = function(_, key) return values[key] end,
            get_keys = function() local keys = {}; for key in pairs(values) do keys[#keys + 1] = key end; return keys end,
        } } }
        _G.kong = {
            request = { get_path = function() return "/ee/v1/chat/completions" end },
            node = { get_id = function() return "node-1" end },
            log = { err = function() end },
            response = { get_status = function() return 200 end, exit = function(status) return status end },
        }
        conf = { route_prefix = "/ee", model_routes = {
            { model = "chat", targets = { { upstream = "a", upstream_model = "real", max_inflight_requests = 2 } } },
        } }
        ctx = { request_model = "chat", is_stream = false }
        metrics = assert(loadfile(spec_dir .. "../metrics.lua"))()
        metrics.configure({ conf })
    end)

    it("does not precreate counters without a selected target", function()
        metrics.collect()
        for key in pairs(series.neutree_route_completed_requests_total) do
            assert.is_not_nil(key:find("|a|real|", 1, true))
        end
    end)

    it("records final client failure without a selected target once, with no success duration", function()
        kong.response.get_status = function() return 503 end
        metrics.log(conf, ctx); metrics.log(conf, ctx)
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|chat|node-1|||non_stream|503"])
        assert.is_nil(next(series.neutree_route_request_duration_seconds))
    end)

    it("separates exact failure codes without recording success durations", function()
        for _, status in ipairs({ 400, 429, 500, 503 }) do
            kong.response.get_status = function() return status end
            metrics.log(conf, { request_model = "chat", is_stream = false })
            assert.are.equal(1, series.neutree_route_completed_requests_total[
                "/ee|chat|node-1|||non_stream|" .. status])
        end
        assert.is_nil(next(series.neutree_route_request_duration_seconds))
    end)

    it("records unavailable status as unknown without a success duration", function()
        kong.response.get_status = function() return nil end
        metrics.log(conf, ctx)
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|chat|node-1|||non_stream|unknown"])
        assert.is_nil(next(series.neutree_route_request_duration_seconds))
    end)

    it("bounds unconfigured, missing and invalid model names to unknown", function()
        for i = 1, 100 do metrics.log(conf, { request_model = "unknown-" .. i }) end
        for _, model in ipairs({ "", false, 123, {} }) do metrics.log(conf, { request_model = model }) end
        metrics.log(conf, {})
        assert.are.equal(105, series.neutree_route_completed_requests_total["/ee|unknown|node-1|||unknown|200"])
    end)

    it("excludes model lists and unrelated paths even before access executes", function()
        kong.response.get_status = function() return 401 end
        metrics.log(conf, {})
        for _, path in ipairs({ "/ee/v1/models", "/ee/anthropic/v1/models", "/ee/health", "/other/v1/chat/completions" }) do
            kong.request.get_path = function() return path end
            metrics.log(conf, {})
        end
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|unknown|node-1|||unknown|401"])
    end)

    it("omits unavailable gauges without losing counters or failing the scrape", function()
        metrics.log(conf, ctx)
        metrics.collect()
        ngx.shared.neutree_ai_gateway_inflight.get = function() return nil, "broken store" end
        assert.are.equal(200, metrics.collect())
        assert.is_nil(next(series.neutree_route_inflight))
        assert.is_nil(next(series.neutree_route_inflight_limit))
        assert.is_nil(next(series.neutree_route_compiled_targets))
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|chat|node-1|||non_stream|200"])
        ngx.shared.neutree_ai_gateway_inflight = nil
        assert.are.equal(200, metrics.collect())
    end)

    it("retains the previous configuration if replacement construction fails", function()
        metrics.configure({ { model_routes = { false } } })
        metrics.collect()
        assert.are.equal(1, series.neutree_route_compiled_targets["/ee|chat|node-1"])
    end)

    it("uses the in-flight request configuration after a model is removed", function()
        metrics.configure({})
        metrics.log(conf, ctx)
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|chat|node-1|||non_stream|200"])
    end)

    it("returns 503 if the exporter fails", function()
        package.loaded["kong.plugins.prometheus.exporter"].collect = function() error("export failed") end
        assert.are.equal(503, metrics.collect())
    end)

    it("retains draining inflight after removal but stops publishing a current limit", function()
        local key = routing.target_key({ scope = "/ee", route = conf.model_routes[1] }, conf.model_routes[1].targets[1])
        values[key] = 2
        metrics.collect()
        assert.are.equal(2, series.neutree_route_inflight_limit["/ee|chat|node-1|a|real"])
        metrics.configure({})
        metrics.collect()
        assert.are.equal(2, series.neutree_route_inflight["/ee|chat|node-1|a|real"])
        assert.is_nil(next(series.neutree_route_inflight_limit))
        values[key] = 0
        metrics.collect()
        assert.is_nil(next(series.neutree_route_inflight))
    end)

    it("scrapes current gauges without reseeding counters", function()
        metrics.collect()
        local seeded = increments
        local key = routing.target_key({ scope = "/ee", route = conf.model_routes[1] }, conf.model_routes[1].targets[1])
        values[key] = 2
        metrics.collect()
        assert.are.equal(2, series.neutree_route_inflight["/ee|chat|node-1|a|real"])
        values[key] = 1
        metrics.collect()
        assert.are.equal(1, series.neutree_route_inflight["/ee|chat|node-1|a|real"])
        assert.are.equal(seeded, increments)
        metrics.log(conf, ctx)
        assert.are.equal(1.25, series.neutree_route_request_duration_seconds["/ee|chat|node-1|||non_stream"])
    end)

    it("returns unavailable when exporter initialization failed", function()
        enabled = false
        metrics = assert(loadfile(spec_dir .. "../metrics.lua"))()
        metrics.configure({ conf })
        assert.are.equal(503, metrics.collect())
    end)

    it("counts early failures even when model routes are empty", function()
        conf.model_routes = {}
        kong.response.get_status = function() return 400 end
        metrics.log(conf, {})
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|unknown|node-1|||unknown|400"])
        assert.is_nil(next(series.neutree_route_request_duration_seconds))
    end)

    it("does not count legacy configurations without model routes", function()
        conf.model_routes = nil
        metrics.log(conf, ctx)
        assert.is_nil(series.neutree_route_completed_requests_total["/ee|chat|node-1|||non_stream|200"])
    end)
end)
