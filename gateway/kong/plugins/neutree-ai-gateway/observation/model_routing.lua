local routing = require("kong.plugins.neutree-ai-gateway.routing")
local M = {}
local models, targets, routes = {}, {}, {}

function M.configure(configs)
    local next_models, next_targets, next_routes = {}, {}, {}
    for _, conf in ipairs(configs or {}) do
        if conf.model_routes then
            local scope = conf.route_prefix or ""
            if conf.route_id then next_routes[conf.route_id] = conf end
            for _, route in ipairs(conf.model_routes) do
                next_models[#next_models + 1] = {
                    endpoint = scope, virtual_model = route.model, target_count = #(route.targets or {}),
                }
                for _, target in ipairs(route.targets or {}) do
                    next_targets[routing.target_key({ scope = scope, route = route }, target)] = {
                        endpoint = scope, virtual_model = route.model,
                        upstream = target.upstream, upstream_model = target.upstream_model,
                        limit = target.max_inflight_requests or 0,
                    }
                end
            end
        end
    end
    -- Build first, then replace. Readers always see one complete configuration;
    -- metrics reads it at scrape time, without another cache or generation ID.
    models, targets, routes = next_models, next_targets, next_routes
end

function M.start(conf)
    if not conf.model_routes then return end
    local event = kong.ctx.shared.neutree_ai_gateway_model_routing or {}
    kong.ctx.shared.neutree_ai_gateway_model_routing = event
    event.request = { endpoint = conf.route_prefix or "" }
    event.routing = {}
end

function M.model(conf, model, stream)
    local event = kong.ctx.shared.neutree_ai_gateway_model_routing
    if not conf.model_routes or not event then return end
    local request = event.request
    request.virtual_model = type(model) == "string" and model or nil
    request.request_mode = stream and "stream" or "non_stream"
    request.model_configured = false
    -- Use this request's configuration, so removal during an in-flight request
    -- does not retroactively turn a known model into an unknown one.
    for _, route in ipairs(conf.model_routes) do
        if route.model == model then request.model_configured = true; break end
    end
end

function M.target(target)
    local event = kong.ctx.shared.neutree_ai_gateway_model_routing
    if not event or not event.routing then return end
    event.routing.selected_target = { upstream = target.upstream, upstream_model = target.upstream_model }
end

local function resolve(route_id, path)
    local conf = routes[route_id]
    if not conf then return end
    local prefix = conf.route_prefix or ""
    if path:sub(1, #prefix) ~= prefix then return end
    local suffix = path:sub(#prefix + 1)
    local inference = suffix == "/anthropic/v1/messages" or suffix == "/anthropic/v1/messages/"
    if not inference then
        for _, endpoint in ipairs({ "/v1/chat/completions", "/v1/embeddings", "/v1/rerank" }) do
            if suffix == endpoint and (not conf.route_type or conf.route_type == endpoint) then
                inference = true
                break
            end
        end
    end
    if inference then return { request = { endpoint = prefix }, routing = {} } end
end

-- Request facts only; the collector decides which outcomes enter each metric.
function M.request()
    local event = kong.ctx.shared.neutree_ai_gateway_model_routing
    if not event then
        local route = kong.router.get_route()
        if route then event = resolve(route.id, kong.request.get_path()) end
    end
    if not event or not event.routing or not event.request then return end
    local request = event.request
    local target = event.routing.selected_target
    return {
        endpoint = request.endpoint,
        virtual_model = request.virtual_model,
        model_configured = request.model_configured,
        request_mode = request.request_mode,
        upstream = target and target.upstream,
        upstream_model = target and target.upstream_model,
        gateway_instance = kong.node.get_id(),
        status_code = kong.response.get_status(),
        duration_seconds = tonumber(ngx.var.request_time),
    }
end

function M.snapshot()
    local shared = ngx.shared.neutree_ai_gateway_inflight
    if not shared then return nil, "routing concurrency dictionary is unavailable" end
    local observations = {}
    for key, target in pairs(targets) do
        local value, err = shared:get(key)
        if err then return nil, err end
        observations[#observations + 1] = {
            endpoint = target.endpoint, virtual_model = target.virtual_model,
            upstream = target.upstream, upstream_model = target.upstream_model,
            limit = target.limit, inflight = value or 0,
        }
    end
    -- Removed targets keep their live leases, but no longer have a current limit.
    -- This is the admission ledger: observation must never reset or delete it.
    for _, key in ipairs(shared:get_keys(0)) do
        if not targets[key] then
            local scope, model, upstream, upstream_model = key:match("^([^%z]*)%z([^%z]*)%z([^%z]*)%z([^%z]*)$")
            local value = scope and shared:get(key)
            if type(value) == "number" and value > 0 then
                observations[#observations + 1] = {
                    endpoint = scope, virtual_model = model, upstream = upstream,
                    upstream_model = upstream_model, inflight = value,
                }
            end
        end
    end
    return { models = models, targets = observations, gateway_instance = kong.node.get_id() }
end

return M
