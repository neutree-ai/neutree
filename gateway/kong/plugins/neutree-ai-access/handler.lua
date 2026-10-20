-- neutree-ai-access: per-consumer access enforcement for an API key.
--
-- The management plane reconciles each API key's static limits onto its Kong
-- consumer as this plugin's config (no per-request control-plane call).
-- Enforced from local config:
--   * disabled        -> 403 key_disabled
--   * allowed_models  -> 403 model_not_permitted. Each entry is endpoint-scoped
--                        ({ model, type?, endpoint_name? }); the request model AND
--                        the IE/EE endpoint it hit must match some entry. Empty
--                        type/endpoint_name = any endpoint of that model. An unset
--                        list means unrestricted; an empty [] means deny-all.
--                        Model-list requests reach the endpoint, then their
--                        collection response is filtered with these same rules.
--                        Disabled keys receive key_disabled from access() or,
--                        when an EE list was already synthesized, response filters.
--   * concurrency     -> 429 concurrency_exceeded (in-flight counter)
--   * rate_limits     -> 429 rate_limit_exceeded (fixed window per window)
-- The plugin is attached only when the key actually has access limits, so an
-- unconfigured key has no plugin and is unrestricted (by design).

local cjson = require("cjson.safe")

local AccessHandler = {
    PRIORITY = 895,
    VERSION = "1.0.0",
}

local WINDOW_SECONDS = { second = 1, minute = 60, hour = 3600, day = 86400 }
-- TTL on the in-flight concurrency counter so stale per-key entries expire
-- instead of accumulating forever (it is far longer than any single request).
local CONCURRENCY_TTL = 3600

local function consumer_id()
    local c = kong.client.get_consumer()
    if c and c.custom_id and c.custom_id ~= "" then
        return c.custom_id
    end
    return nil
end

local function request_model()
    -- Prefer the client-facing model stashed by neutree-ai-gateway (priority
    -- 900) before it rewrites the request body for model mapping. This plugin
    -- (895) runs after that rewrite, so re-reading the raw body here is
    -- unreliable (it can come back nil) — which would make any key with an
    -- allowed_models list reject every request. Fall back to the body for routes
    -- without the gateway plugin.
    local stashed = kong.ctx.shared and kong.ctx.shared.neutree_request_model
    if type(stashed) == "string" and stashed ~= "" then
        return stashed
    end
    local raw = kong.request.get_raw_body()
    if raw and raw ~= "" then
        local decoded = cjson.decode(raw)
        if type(decoded) == "table" and type(decoded.model) == "string" then
            return decoded.model
        end
    end
    return nil
end

-- is_model_list_request reports whether this request asks for the endpoint's
-- model list rather than for a model to be served. An enabled key reaches the
-- endpoint because the request names no one model; body_filter then applies the
-- allowlist semantics to each model in the collection response. A disabled key
-- is normally rejected in access(), while response filters cover an EE list that
-- neutree-ai-gateway already synthesized before this plugin's access() ran.
--
-- Matched on the end of the request path, because the client-facing route prefix
-- (/workspace/<ws>/endpoint/<ep>, .../external-endpoint/<ee>) precedes the API
-- suffix, and the Anthropic alias ends with the same suffix. Only the end of the
-- path decides: /v1/modelsx and /v1/models-x stay enforced.
-- kong.request.get_path() is the normalized, query-free path -- the same value
-- neutree-ai-gateway matches its own route suffix against -- so a request cannot
-- arrive here looking like a model list and still be routed as an inference
-- call further down.
--
-- The retrieve form (/v1/models/{id}) is deliberately not included: it names a
-- model, so the allowlist keeps judging it.
local MODELS_PATH_SUFFIX = "/v1/models"

local function is_model_list_request()
    -- GET only: listing is a read, and no engine defines a mutating call at this
    -- path, so a body-less request of another method must not buy the exemption.
    if kong.request.get_method() ~= "GET" then
        return false
    end

    local path = kong.request.get_path() or ""

    -- Tolerate the trailing-slash spelling of the same endpoint, which is the
    -- other shape neutree-ai-gateway answers a model list for.
    if path:sub(-1) == "/" then
        path = path:sub(1, -2)
    end

    return path:sub(-#MODELS_PATH_SUFFIX) == MODELS_PATH_SUFFIX
end

-- allow_match reports whether the request (model + the IE/EE endpoint it hit) is
-- permitted by the endpoint-scoped allowlist. An entry permits the request when
-- its `model` matches AND — for each of `type` / `endpoint_name` that the entry
-- pins — the endpoint identity stashed by neutree-ai-gateway matches. An entry
-- with empty type/endpoint_name is "any endpoint serving this model" (legacy
-- name-only keys, migrated to this shape). An empty list permits nothing (deny-all).
-- A pinned dimension matches when it is unset (= any) or equals the endpoint the
-- request actually hit. "Unset" is anything that isn't a non-empty string: nil,
-- or cjson.null (userdata) should a JSON null ever reach here — so a null
-- type/endpoint_name reads as "any", never as a pin that can't match.
--
-- endpoint_name is matched bare (not workspace-qualified): an API key only
-- reaches endpoints in its own workspace (the route ACL rejects anything else
-- before this plugin), and names are unique within a workspace, so there is no
-- cross-workspace collision to guard against.
local function pin_ok(pin, actual)
    return type(pin) ~= "string" or pin == "" or pin == actual
end

local function allow_match(list, model, ep_type, ep_name)
    if type(list) ~= "table" then
        return false
    end
    for _, entry in ipairs(list) do
        if type(entry) == "table" and entry.model == model
            and pin_ok(entry.type, ep_type)
            and pin_ok(entry.endpoint_name, ep_name) then
            return true
        end
    end
    return false
end

local function json_array()
    return setmetatable({}, cjson.array_mt)
end

local function filter_model_list_response(body, allowed_models, ep_type, ep_name)
    local response = cjson.decode(body)
    if type(response) ~= "table" or type(response.data) ~= "table" then
        return body
    end

    -- OpenAI lists carry object=list; Anthropic lists carry pagination fields.
    -- Do not reinterpret another successful JSON response that happens to have a
    -- data field as a model collection.
    if response.object ~= "list" and response.has_more == nil then
        return body
    end

    local visible = json_array()
    for _, model in ipairs(response.data) do
        if type(model) == "table" and type(model.id) == "string"
            and allow_match(allowed_models, model.id, ep_type, ep_name) then
            visible[#visible + 1] = model
        end
    end
    response.data = visible

    -- Anthropic's cursor fields describe the returned data, not the upstream's
    -- unfiltered page. Preserve their presence while keeping them coherent.
    if response.first_id ~= nil then
        response.first_id = visible[1] and visible[1].id or nil
    end
    if response.last_id ~= nil then
        response.last_id = visible[#visible] and visible[#visible].id or nil
    end

    return cjson.encode(response) or body
end

local function is_successful_model_list_response()
    return is_model_list_request() and kong.response.get_status() == 200
end

local function should_filter_model_list_response(conf)
    return type(conf.allowed_models) == "table" and is_successful_model_list_response()
end

local function counter_dict()
    -- Only the dedicated rate-limiting counters dict; return nil (callers fail
    -- open) rather than writing into kong_locks, which Kong uses for locking.
    return ngx.shared.kong_rate_limiting_counters
end

local function access_error(code, message)
    return {
        error = { message = message, type = "not_permitted", code = code },
    }
end

local function deny403(code, message)
    return kong.response.exit(403, access_error(code, message))
end

function AccessHandler:access(conf)
    -- 1) Disabled: hard stop.
    if conf.disabled then
        return deny403("key_disabled", "This API key is disabled")
    end

    -- 2) Model allowlist: an absent field (nil / JSON null = cjson.null) means
    --    "unrestricted"; a list (any JSON array, INCLUDING an empty []) means the
    --    request model must be present and in it. An explicit empty [] therefore
    --    denies every model (deny-all). Guard on the value being a table so null
    --    stays unrestricted while [] enforces. A model list is not one model
    --    call, so it reaches the endpoint here; body_filter narrows its response
    --    to the models visible through this key, including an empty [] deny-all.
    if type(conf.allowed_models) == "table" and not is_model_list_request() then
        local model = request_model()
        local ep_type = kong.ctx.shared and kong.ctx.shared.neutree_endpoint_type
        local ep_name = kong.ctx.shared and kong.ctx.shared.neutree_endpoint_name
        if not model or not allow_match(conf.allowed_models, model, ep_type, ep_name) then
            return deny403("model_not_permitted", "Model not permitted for this API key")
        end
    end

    local key = consumer_id()

    -- Counter-based limits below need an identified consumer; without one, fail
    -- open rather than throttle every such request against a shared bucket.
    if not key then
        return
    end

    -- 3) Concurrency: increment in-flight, remember it for log() to decrement.
    if conf.concurrency and tonumber(conf.concurrency) and tonumber(conf.concurrency) > 0 then
        local dict = counter_dict()
        if dict then
            local cc_key = "neutree_cc:" .. key
            local inflight, cerr = dict:incr(cc_key, 1, 0, CONCURRENCY_TTL)
            if not inflight then
                -- Counting unavailable (e.g. dict full): fail open and do not set
                -- cc_key, so log() won't decrement a counter we never incremented.
                kong.log.warn("neutree-ai-access: concurrency incr: ", tostring(cerr))
            else
                kong.ctx.plugin.cc_key = cc_key
                if inflight > tonumber(conf.concurrency) then
                    dict:incr(cc_key, -1)
                    kong.ctx.plugin.cc_key = nil
                    return kong.response.exit(429, {
                        error = {
                            message = "Concurrency limit exceeded for this API key",
                            type = "rate_limited",
                            code = "concurrency_exceeded",
                        },
                    })
                end
            end
        end
    end

    -- 4) Rate limits: fixed window per window.
    if type(conf.rate_limits) == "table" and #conf.rate_limits > 0 then
        local dict = counter_dict()
        if dict then
            local now = ngx.now()
            for _, rl in ipairs(conf.rate_limits) do
                local limit = tonumber(rl.limit)
                local wsec = WINDOW_SECONDS[rl.window]
                if limit and wsec then
                    local window_start = math.floor(now / wsec) * wsec
                    local rk = "neutree_rl:" .. key .. ":" .. rl.window .. ":" .. window_start
                    -- TTL = remaining time in this window so the counter expires
                    -- when the window ends rather than up to a full window later.
                    local rk_ttl = math.max(1, math.ceil(window_start + wsec - now))
                    -- Atomically initialize the window counter so two concurrent
                    -- first-requests don't both reset it to 1 (undercounting):
                    -- add() only succeeds for the first caller, then incr().
                    dict:add(rk, 0, rk_ttl)
                    local newval, err = dict:incr(rk, 1)
                    if not newval then
                        -- Counting unavailable (e.g. dict full) -> fail open.
                        if err then
                            kong.log.warn("neutree-ai-access: counter incr: ", err)
                        end
                        newval = 0
                    end
                    if newval > limit then
                        if kong.ctx.plugin.cc_key then
                            dict:incr(kong.ctx.plugin.cc_key, -1)
                            kong.ctx.plugin.cc_key = nil
                        end
                        kong.response.set_header("Retry-After", math.max(1, math.ceil(window_start + wsec - now)))
                        return kong.response.exit(429, {
                            error = {
                                message = "Request rate limit exceeded for this API key",
                                type = "rate_limited",
                                code = "rate_limit_exceeded",
                            },
                        })
                    end
                end
            end
        end
    end
end

function AccessHandler:header_filter(conf)
    if not is_successful_model_list_response() then
        return
    end

    if conf.disabled then
        -- EE lists are generated by neutree-ai-gateway (priority 900), whose
        -- access-phase exit prevents this consumer plugin (895) from rejecting
        -- there. Header/body filters still run after that exit, so preserve the
        -- normal disabled-key response without moving the plugin priority.
        kong.ctx.plugin.disabled_model_list_response = true
        kong.response.set_status(403)
        kong.response.set_header("Content-Type", "application/json")
        kong.response.clear_header("Content-Length")
        return
    end

    if should_filter_model_list_response(conf) then
        -- body_filter can shorten the collection, so the upstream or synthetic
        -- response's original length would be stale by the time it reaches the
        -- client. Kong cannot revise it after this phase.
        kong.response.clear_header("Content-Length")
    end
end

function AccessHandler:body_filter(conf)
    if kong.ctx.plugin.disabled_model_list_response then
        local body = kong.response.get_raw_body()
        if not body then
            return
        end

        kong.response.set_raw_body(cjson.encode(access_error(
            "key_disabled",
            "This API key is disabled"
        )))
        return
    end

    if not should_filter_model_list_response(conf) then
        return
    end

    local body = kong.response.get_raw_body()
    if not body then
        return
    end

    local shared = kong.ctx.shared or {}
    kong.response.set_raw_body(filter_model_list_response(
        body,
        conf.allowed_models,
        shared.neutree_endpoint_type,
        shared.neutree_endpoint_name
    ))
end

function AccessHandler:log(_)
    local cc_key = kong.ctx.plugin.cc_key
    if cc_key then
        local dict = counter_dict()
        if dict then
            dict:incr(cc_key, -1)
        end
    end
end

return AccessHandler
