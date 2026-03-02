local cjson = require("cjson.safe")

local ModelRouterHandler = {
    PRIORITY = 1100,
    VERSION = "0.0.1",
}

-- ============================================================
-- Phase: access
-- ============================================================
function ModelRouterHandler:access(conf)
    -- Handle GET /v1/models — return available models from config
    local request_path = kong.request.get_path()
    local prefix = conf.route_prefix or ""
    local suffix = request_path
    if prefix ~= "" then
        local prefix_pattern = "^" .. prefix:gsub("([%-%.%+%[%]%(%)%$%^%%%?%*])", "%%%1")
        suffix = request_path:gsub(prefix_pattern, "", 1)
    end

    if (suffix == "/v1/models" or suffix == "/v1/models/") and kong.request.get_method() == "GET" then
        local models = {}
        for _, entry in ipairs(conf.upstreams) do
            for model_name, _ in pairs(entry.model_mapping) do
                models[#models + 1] = {
                    id = model_name,
                    object = "model",
                    created = 0,
                    owned_by = "external-endpoint",
                }
            end
        end
        return kong.response.exit(200, {
            object = "list",
            data = models,
        })
    end

    local method = kong.request.get_method()
    if (suffix == "/anthropic/v1/models" or suffix == "/anthropic/v1/models/") and method == "GET" then
        local models = {}
        for _, entry in ipairs(conf.upstreams) do
            for model_name, _ in pairs(entry.model_mapping) do
                models[#models + 1] = {
                    id = model_name,
                    type = "model",
                    display_name = model_name,
                    created_at = "2025-01-01T00:00:00Z",
                }
            end
        end
        return kong.response.exit(200, {
            data = models,
            has_more = false,
            first_id = models[1] and models[1].id or nil,
            last_id = models[#models] and models[#models].id or nil,
        })
    end

    -- Read and parse request body to extract model field
    local body = kong.request.get_raw_body()
    if not body or body == "" then
        return kong.response.exit(400, {
            error = {
                message = "Request body is required for model routing",
                type = "invalid_request_error",
            },
        })
    end

    local json, err = cjson.decode(body)
    if err or not json then
        return kong.response.exit(400, {
            error = {
                message = "Invalid JSON in request body",
                type = "invalid_request_error",
            },
        })
    end

    local model = json.model
    if not model or model == "" then
        return kong.response.exit(400, {
            error = {
                message = "Missing 'model' field in request body",
                type = "invalid_request_error",
            },
        })
    end

    -- Find matching upstream entry by model_mapping keys
    local matched_entry = nil
    for _, entry in ipairs(conf.upstreams) do
        if entry.model_mapping[model] then
            matched_entry = entry
            break
        end
    end

    if not matched_entry then
        return kong.response.exit(400, {
            error = {
                message = "No upstream configured for model: " .. model,
                type = "invalid_request_error",
            },
        })
    end

    -- Switch upstream using Kong PDK
    kong.service.set_target(matched_entry.host, matched_entry.port)
    kong.service.request.set_scheme(matched_entry.scheme)

    -- Build upstream path: base path + suffix (suffix already computed above)
    if suffix == "" then
        suffix = "/"
    end
    local upstream_base = matched_entry.path or "/"
    if upstream_base == "/" then
        upstream_base = ""
    end
    upstream_base = upstream_base:gsub("/$", "")

    local final_path = upstream_base .. suffix
    if final_path == "" then
        final_path = "/"
    end

    kong.service.request.set_path(final_path)

    -- Set auth header if configured
    if matched_entry.auth_header and matched_entry.auth_header ~= "" then
        kong.service.request.set_header("Authorization", matched_entry.auth_header)
    end

    -- Rewrite model field to upstream model name
    json.model = matched_entry.model_mapping[model]
    local new_body = cjson.encode(json)
    if new_body then
        kong.service.request.set_raw_body(new_body)
    end

    -- Store context for downstream plugins (e.g., anthropic format plugin)
    kong.ctx.shared.model_router_resolved = true
    kong.ctx.shared.model_router_upstream_base_path = matched_entry.path or "/"
end

return ModelRouterHandler
