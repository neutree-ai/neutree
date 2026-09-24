-- Request observation remains private to the AI Gateway plugin.
local M = {}

function M.start(conf)
    if not conf.model_routes then return end
    local event = kong.ctx.plugin.observation or {}
    kong.ctx.plugin.observation = event
    event.request = { endpoint = conf.route_prefix or "" }
    event.routing = {}
end

function M.model(conf, model, stream)
    local event = kong.ctx.plugin.observation
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
    local event = kong.ctx.plugin.observation
    if not event or not event.routing then return end
    event.routing.selected_target = { upstream = target.upstream, upstream_model = target.upstream_model }
end

local function resolve(conf, path)
    if not conf.model_routes then return end
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
function M.request(conf)
    local event = kong.ctx.plugin.observation
    if not event then
        event = resolve(conf, kong.request.get_path())
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

return M
