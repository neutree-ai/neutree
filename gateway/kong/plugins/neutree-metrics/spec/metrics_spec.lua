local spec_dir = (debug.getinfo(1, "S").source:sub(2)):match("(.*/)")
local routing = assert(loadfile(spec_dir .. "../../neutree-ai-gateway/routing.lua"))()

describe("independent metrics plugin", function()
    local metrics, observation, sources, values, series, conf, enabled, increments, errors, recording_error
    local function request(model, status, target)
        kong.ctx = { shared = {}, plugin = {} }
        observation.start(conf)
        observation.model(conf, model, false)
        if target then observation.target(target) end
        kong.response.get_status = function() return status or 200 end
    end
    before_each(function()
        values, series, enabled, increments, errors = {}, {}, true, 0, {}
        recording_error = false
        local registry = { LOCAL_STORAGE = true }
        local function metric(_, name)
            series[name] = series[name] or {}
            local data = series[name]
            return {
                inc = function(_, value, labels)
                    if recording_error then error("exporter write failed") end
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
        package.loaded["kong.plugins.neutree-metrics.sources"] = nil
        package.loaded["kong.plugins.neutree-ai-gateway.observation"] = nil
        package.loaded["kong.plugins.neutree-ai-gateway.routing"] = routing
        _G.ngx = { var = { request_time = "1.25" }, shared = { neutree_ai_gateway_inflight = {
            get = function(_, key) return values[key] end,
            get_keys = function() local keys = {}; for key in pairs(values) do keys[#keys + 1] = key end; return keys end,
        } } }
        _G.kong = {
            ctx = { shared = {}, plugin = {} },
            node = { get_id = function() return "node-1" end },
            log = { err = function(...) errors[#errors + 1] = {...} end },
            response = { get_status = function() return 200 end, exit = function(status) return status end },
            router = { get_route = function() return { id = "route-1" } end },
            request = { get_path = function() return "/ee/v1/chat/completions" end },
        }
        conf = { route_id = "route-1", route_prefix = "/ee", model_routes = {
            { model = "chat", targets = { { upstream = "a", upstream_model = "real", max_inflight_requests = 2 } } },
            { model = "empty", targets = {} },
        } }
        -- Load the central declaration before Gateway configures its provider.
        sources = require("kong.plugins.neutree-metrics.sources")
        observation = require("kong.plugins.neutree-ai-gateway.observation")
        observation.configure({ conf })
        metrics = assert(loadfile(spec_dir .. "../metrics.lua"))()
        metrics.init()
    end)

    it("also loads Gateway before the declaration without registering itself", function()
        package.loaded["kong.plugins.neutree-metrics.sources"] = nil
        package.loaded["kong.plugins.neutree-ai-gateway.observation"] = nil
        observation = require("kong.plugins.neutree-ai-gateway.observation")
        assert.is_nil(package.loaded["kong.plugins.neutree-metrics.sources"])
        observation.configure({ conf })
        sources = require("kong.plugins.neutree-metrics.sources")
        assert.are.equal(observation.snapshot, sources["model-routing"].snapshot)
        assert.are.equal(observation.resolve, sources["model-routing"].resolve)
        assert.are.equal(2, #sources["model-routing"].snapshot().models)
        observation.configure(nil)
        assert.are.equal(0, #sources["model-routing"].snapshot().models)
        assert.is_nil(sources["model-routing"].resolve("route-1", "/ee/v1/chat/completions"))
    end)

    it("records final client failure once without success duration", function()
        request("chat", 503)
        metrics.log(); metrics.log()
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|chat|node-1|||non_stream|503"])
        assert.is_nil(next(series.neutree_route_request_duration_seconds))
    end)

    it("keeps exact failure codes and final successful request durations", function()
        for _, status in ipairs({ 400, 429, 500, 503 }) do
            request("chat", status); metrics.log()
            assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|chat|node-1|||non_stream|" .. status])
        end
        assert.is_nil(next(series.neutree_route_request_duration_seconds))
        request("chat", 200, conf.model_routes[1].targets[1]); metrics.log()
        assert.are.equal(1.25, series.neutree_route_request_duration_seconds["/ee|chat|node-1|a|real|non_stream"])
    end)

    it("bounds arbitrary, missing and invalid model names to unknown", function()
        for i = 1, 100 do request("random-" .. i, 400); metrics.log() end
        for _, model in ipairs({ false, {}, 123, "" }) do request(model, 400); metrics.log() end
        request(nil, 400); metrics.log()
        assert.are.equal(105, series.neutree_route_completed_requests_total["/ee|unknown|node-1|||non_stream|400"])
        local count = 0; for _ in pairs(series.neutree_route_completed_requests_total) do count = count + 1 end
        assert.are.equal(1, count)
        request("empty", 400); metrics.log()
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|empty|node-1|||non_stream|400"])
    end)

    it("keeps in-flight request identity across configuration removal", function()
        request("chat", 200, conf.model_routes[1].targets[1])
        observation.configure(nil)
        metrics.log()
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|chat|node-1|a|real|non_stream|200"])
    end)

    it("reads config additions and removals without metrics configure or a generation", function()
        metrics.collect()
        assert.are.equal(0, series.neutree_route_compiled_targets["/ee|empty|node-1"])
        conf.model_routes[#conf.model_routes + 1] = { model = "new", targets = {} }
        observation.configure({conf})
        metrics.collect()
        assert.are.equal(0, series.neutree_route_compiled_targets["/ee|new|node-1"])
        observation.configure(nil)
        metrics.collect()
        assert.is_nil(next(series.neutree_route_compiled_targets))
        assert.is_nil(metrics.configure)
    end)

    it("retains draining inflight after removal without a current limit", function()
        local key = routing.target_key({ scope = "/ee", route = conf.model_routes[1] }, conf.model_routes[1].targets[1])
        values[key] = 2
        metrics.collect()
        assert.are.equal(2, series.neutree_route_inflight_limit["/ee|chat|node-1|a|real"])
        observation.configure({})
        metrics.collect()
        assert.are.equal(2, series.neutree_route_inflight["/ee|chat|node-1|a|real"])
        assert.is_nil(next(series.neutree_route_inflight_limit))
        assert.are.equal(2, values[key])
        values[key] = 0; metrics.collect()
        assert.is_nil(next(series.neutree_route_inflight))
    end)

    it("scrapes live gauges without reinitializing counters or creating unassigned zeros", function()
        metrics.collect()
        local seeded = increments
        local key = routing.target_key({ scope = "/ee", route = conf.model_routes[1] }, conf.model_routes[1].targets[1])
        values[key] = 2; metrics.collect()
        assert.are.equal(2, series.neutree_route_inflight["/ee|chat|node-1|a|real"])
        values[key] = 1; metrics.collect()
        assert.are.equal(1, series.neutree_route_inflight["/ee|chat|node-1|a|real"])
        assert.are.equal(seeded, increments)
        for name in pairs(series.neutree_route_completed_requests_total) do
            assert.is_not_nil(name:find("|a|real|", 1, true))
        end
    end)

    it("attributes early rejection to its route without parsing the body", function()
        kong.response.get_status = function() return 401 end
        metrics.log()
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|unknown|node-1|||unknown|401"])
        for _, suffix in ipairs({ "/v1/models", "/anthropic/v1/models", "/health", "/anything" }) do
            kong.ctx.plugin = {}
            kong.request.get_path = function() return "/ee" .. suffix end
            metrics.log()
        end
        assert.are.equal(1, series.neutree_route_completed_requests_total["/ee|unknown|node-1|||unknown|401"])
    end)

    it("ignores unrelated routes and legacy gateway configurations", function()
        conf.model_routes = nil; observation.configure({ conf })
        request("chat"); metrics.log()
        assert.is_nil(next(series.neutree_route_completed_requests_total))
    end)

    it("consumes another provider through the same observation contract", function()
        sources["model-routing"] = nil
        sources.other = { snapshot = function() return { models = {
            { endpoint = "/other", virtual_model = "other-chat", target_count = 0 },
        }, targets = {} } end }
        kong.ctx.shared.neutree_observation = {
            request = { endpoint = "/other", virtual_model = "other-chat", model_configured = true },
            routing = {}, quota = { allowed = true },
        }
        metrics.log(); metrics.log(); metrics.collect()
        assert.are.equal(1, series.neutree_route_completed_requests_total["/other|other-chat|node-1|||unknown|200"])
        assert.are.equal(0, series.neutree_route_compiled_targets["/other|other-chat|node-1"])
    end)

    it("isolates source exceptions and returned errors, omitting unavailable gauges", function()
        metrics.collect()
        ngx.shared.neutree_ai_gateway_inflight = nil
        sources.broken = { snapshot = function() error("broken source") end }
        sources.other = { snapshot = function() return { models = {
            { endpoint = "/other", virtual_model = "ok", target_count = 1 },
        } } end }
        assert.are.equal(200, metrics.collect())
        assert.is_nil(next(series.neutree_route_inflight))
        assert.are.equal(1, series.neutree_route_compiled_targets["/other|ok|node-1"])
        assert.are.equal(2, #errors)
    end)

    it("does not throw when exporter is unavailable", function()
        enabled = false
        metrics = assert(loadfile(spec_dir .. "../metrics.lua"))()
        assert.is_false(metrics.init())
        request("chat", 200); assert.has_no.errors(metrics.log)
        assert.are.equal(503, metrics.collect())
        assert.is_nil(next(series.neutree_route_completed_requests_total))
    end)

    it("isolates exporter write failures from subsequent logging", function()
        request("chat", 200, conf.model_routes[1].targets[1])
        recording_error = true
        assert.has_no.errors(metrics.log)
        assert.are.equal(1, #errors)
        assert.are.equal("a", kong.ctx.shared.neutree_observation.routing.selected_target.upstream)
        assert.is_nil(next(series.neutree_route_completed_requests_total))
    end)

    it("publishes facts without exposing leases or needing an exporter", function()
        enabled = false
        request("chat", 200, { upstream = "a", upstream_model = "real", lease = function() end })
        assert.are.same({ upstream = "a", upstream_model = "real" }, kong.ctx.shared.neutree_observation.routing.selected_target)
        assert.is_nil(next(series.neutree_route_completed_requests_total))
    end)
end)
