local exporter = require("kong.plugins.prometheus.exporter")
local routing = require("kong.plugins.neutree-ai-gateway.routing")

local M = {}
local requests, inflight
local targets = {}

local function labels(conf, route, target)
    return { conf.route_prefix, route.model, target.upstream, target.upstream_model }
end

function M.init_worker()
    local prometheus = exporter.get_prometheus()
    if not prometheus then
        return
    end
    local names = { "endpoint", "virtual_model", "upstream", "upstream_model" }
    requests = prometheus:counter("neutree_route_requests_total",
        "Completed requests with a selected routing target", names)
    -- Read the admission counter at scrape time, not a second increment/decrement gauge.
    inflight = prometheus:gauge("neutree_route_inflight",
        "In-flight requests through this Kong instance", names, prometheus.LOCAL_STORAGE)
end

function M.configure(configs)
    targets = {}
    for _, conf in ipairs(configs or {}) do
        for _, route in ipairs(conf.model_routes or {}) do
            for _, target in ipairs(route.targets) do
                targets[#targets + 1] = {
                    key = routing.target_key({ scope = conf.route_prefix, route = route }, target),
                    labels = labels(conf, route, target),
                }
                if requests then
                    requests:inc(0, labels(conf, route, target))
                end
            end
        end
    end
end

function M.log(conf, state)
    if requests and state.route and state.current then
        requests:inc(1, labels(conf, state.route, state.current))
    end
end

function M.collect()
    local shared = ngx.shared.neutree_ai_gateway_inflight
    if not inflight or not shared then
        return kong.response.exit(503, { message = "Routing metrics are not initialized" })
    end
    inflight:reset()
    for _, target in ipairs(targets) do
        inflight:set(shared:get(target.key) or 0, target.labels)
    end
    exporter.collect()
end

return M
