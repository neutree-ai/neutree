-- Model-routing metric definitions and aggregation. Business state is read-only.
local M = {}
local completed, duration, inflight, limits, compiled
local seeded = {}
local names = { "endpoint", "virtual_model", "gateway_instance", "upstream", "upstream_model" }

local function labels(endpoint, model, upstream, upstream_model, instance)
    return { endpoint or "", model, instance, upstream or "", upstream_model or "" }
end

-- Called once per worker by the metrics dispatcher.
function M.init(prometheus)
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
end

local function seed_target(target, seen, instance)
    if target.limit == nil then return end -- draining targets are not configured
    local values = labels(target.endpoint, target.virtual_model, target.upstream, target.upstream_model, instance)
    local key = table.concat(values, "\0")
    seen[key] = true
    if seeded[key] then return end
    for _, mode in ipairs({ "stream", "non_stream", "unknown" }) do
        values[6], values[7] = mode, "200"
        completed:inc(0, values)
    end
end

function M.record(request)
    local model = request.model_configured and request.virtual_model or "unknown"
    if type(model) ~= "string" or model == "" then model = "unknown" end
    local values = labels(request.endpoint, model, request.upstream, request.upstream_model, request.gateway_instance)
    values[6] = (request.request_mode == "stream" or request.request_mode == "non_stream")
        and request.request_mode or "unknown"
    local status = request.status_code
    values[7] = type(status) == "number" and status >= 100 and status <= 599 and status == math.floor(status)
        and tostring(status) or "unknown"
    completed:inc(1, values)
    if values[7]:match("^2%d%d$") then
        -- http-log's latencies.request is Nginx request_time in milliseconds.
        local elapsed = request.duration_seconds
        if type(elapsed) == "number" and elapsed >= 0 then
            values[7] = nil
            duration:observe(elapsed, values)
        end
    end
end

-- Gauges describe the current snapshot, never a previous successful scrape.
function M.collect(snapshot)
    inflight:reset()
    limits:reset()
    compiled:reset()
    if not snapshot then return end
    local instance = snapshot.gateway_instance
    local seen = {}
    for _, target in ipairs(snapshot.targets or {}) do
        local values = labels(target.endpoint, target.virtual_model, target.upstream, target.upstream_model, instance)
        inflight:set(target.inflight, values)
        if target.limit ~= nil then limits:set(target.limit, values) end
        seed_target(target, seen, instance)
    end
    for _, model in ipairs(snapshot.models or {}) do
        compiled:set(model.target_count, { model.endpoint, model.virtual_model, instance })
    end
    -- Track initialized series only; configuration remains owned by the source.
    seeded = seen
end

return M
