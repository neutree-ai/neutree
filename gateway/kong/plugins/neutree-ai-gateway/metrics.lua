-- Routing metrics share Kong's exporter and the admission counter. No second
-- concurrency ledger.
local routing = require("kong.plugins.neutree-ai-gateway.routing")
local M = {}
local registry, completed, duration, inflight, limits, compiled
local targets, models = {}, {}
local names = { "endpoint", "virtual_model", "gateway_instance", "upstream", "upstream_model" }

local function labels(endpoint, model, upstream, upstream_model)
    return { endpoint or "", model, kong.node.get_id(), upstream or "", upstream_model or "" }
end

-- Register once per worker; configuration updates reuse these metric objects.
local function ensure_metrics()
    if registry then return true end
    local prometheus = require("kong.plugins.prometheus.exporter").get_prometheus()
    if not prometheus then return false end
    completed = prometheus:counter("neutree_route_completed_requests_total",
        "Completed logical routing requests, including requests with no selected target",
        { "endpoint", "virtual_model", "gateway_instance", "upstream", "upstream_model", "request_mode", "status_code" })
    duration = prometheus:histogram("neutree_route_request_duration_seconds",
        "Full duration of final HTTP 2xx routing requests, not time to first token",
        { "endpoint", "virtual_model", "gateway_instance", "upstream", "upstream_model", "request_mode" },
        { 0.5, 1, 2, 3, 5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 300, 600 })
    inflight = prometheus:gauge("neutree_route_inflight", "Gateway in-flight requests, all request modes", names, prometheus.LOCAL_STORAGE)
    limits = prometheus:gauge("neutree_route_inflight_limit", "Per-instance configured concurrency limit; zero means unlimited", names, prometheus.LOCAL_STORAGE)
    compiled = prometheus:gauge("neutree_route_compiled_targets", "Targets in the loaded configuration, not upstream health",
        { "endpoint", "virtual_model", "gateway_instance" }, prometheus.LOCAL_STORAGE)
    registry = prometheus
    return true
end

local function seed(endpoint, model, upstream, upstream_model)
    for _, mode in ipairs({ "stream", "non_stream", "unknown" }) do
        local values = labels(endpoint, model, upstream, upstream_model)
        values[6], values[7] = mode, "200"
        completed:inc(0, values)
    end
end

local function configure(new_configs)
    targets, models = {}, {}
    local ready = ensure_metrics()
    for _, conf in ipairs(new_configs or {}) do
        if conf.model_routes then
            local scope = conf.route_prefix or ""
            local declared = {}
            for _, route in ipairs(conf.model_routes) do
                declared[route.model] = #(route.targets or {})
                for _, target in ipairs(route.targets or {}) do
                    targets[routing.target_key({ scope = scope, route = route }, target)] = {
                        labels = labels(scope, route.model, target.upstream, target.upstream_model),
                        limit = target.max_inflight_requests or 0,
                    }
                    if ready then seed(scope, route.model, target.upstream, target.upstream_model) end
                end
            end
            models[scope] = declared
        end
    end
end

local function record(conf, ctx)
    if conf.model_routes == nil or ctx.route_metrics_recorded then return end
    if not ensure_metrics() then return end
    ctx.route_metrics_recorded = true
    local scope = conf.route_prefix or ""
    local model = ctx.request_model or ""
    local target = ctx.routing_state and ctx.routing_state.current
    local values = labels(scope, model, target and target.upstream, target and target.upstream_model)
    values[6] = ctx.is_stream == nil and "unknown" or (ctx.is_stream and "stream" or "non_stream")
    local status = kong.response.get_status()
    values[7] = type(status) == "number" and status >= 100 and status <= 599 and status == math.floor(status)
        and tostring(status) or "unknown"
    completed:inc(1, values)
    if values[7]:match("^2%d%d$") then
        -- http-log's latencies.request is Nginx request_time in milliseconds.
        local elapsed = tonumber(ngx.var.request_time)
        if elapsed and elapsed >= 0 then
            values[7] = nil
            duration:observe(elapsed, values)
        end
    end
end

-- Metrics are diagnostic: a recorder/registration exception must never change
-- routing or prevent the existing log phase from accounting usage.
local function safely(fn, ...)
    local ok, err = pcall(fn, ...)
    if not ok then kong.log.err("routing metrics: ", err) end
    return ok
end

function M.configure(new_configs) safely(configure, new_configs) end
function M.log(conf, ctx) safely(record, conf, ctx) end

function M.collect()
    local shared = ngx.shared.neutree_ai_gateway_inflight
    if not shared or not ensure_metrics() then
        return kong.response.exit(503, { message = "Routing metrics are not initialized" })
    end
    inflight:reset()
    limits:reset()
    compiled:reset()
    for key, target in pairs(targets) do
        inflight:set(shared:get(key) or 0, target.labels)
        limits:set(target.limit, target.labels)
    end
    -- Removed targets with active leases remain observable while draining.
    -- Never delete/reset admission keys: another worker may still hold a lease.
    for _, key in ipairs(shared:get_keys(0)) do
        if not targets[key] then
            local scope, model, upstream, upstream_model = key:match("^([^%z]*)%z([^%z]*)%z([^%z]*)%z([^%z]*)$")
            local value = scope and shared:get(key)
            if type(value) == "number" and value > 0 then
                inflight:set(value, labels(scope, model, upstream, upstream_model))
            end
        end
    end
    for scope, declared in pairs(models) do
        for model, count in pairs(declared) do
            compiled:set(count, { scope, model, kong.node.get_id() })
        end
    end
    require("kong.plugins.prometheus.exporter").collect()
end

return M
