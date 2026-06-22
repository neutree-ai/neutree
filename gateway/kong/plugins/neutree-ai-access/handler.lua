-- neutree-ai-access: per-consumer access enforcement for an API key.
--
-- The management plane reconciles each API key's static limits onto its Kong
-- consumer as this plugin's config (no per-request control-plane call).
-- Enforced from local config:
--   * disabled        -> 403 key_disabled
--   * allowed_models  -> 403 model_not_permitted (only when a non-empty list is
--                        set and the request model is missing/not in it; empty or
--                        unset means unrestricted)
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

local function consumer_id()
    local c = kong.client.get_consumer()
    if c and c.custom_id and c.custom_id ~= "" then
        return c.custom_id
    end
    return "unknown"
end

local function request_model()
    local raw = kong.request.get_raw_body()
    if raw and raw ~= "" then
        local decoded = cjson.decode(raw)
        if type(decoded) == "table" and type(decoded.model) == "string" then
            return decoded.model
        end
    end
    return nil
end

local function list_has(list, value)
    if type(list) ~= "table" then
        return false
    end
    for _, v in ipairs(list) do
        if v == value then
            return true
        end
    end
    return false
end

local function counter_dict()
    return ngx.shared.kong_rate_limiting_counters or ngx.shared.kong_locks
end

local function deny403(code, message)
    return kong.response.exit(403, {
        error = { message = message, type = "not_permitted", code = code },
    })
end

function AccessHandler:access(conf)
    -- 1) Disabled: hard stop.
    if conf.disabled then
        return deny403("key_disabled", "This API key is disabled")
    end

    -- 2) Model allowlist: only enforce when a non-empty list is configured. A
    --    cleared field can arrive as JSON null (cjson.null) and an empty list
    --    means "unrestricted", so guard on a non-empty table rather than non-nil.
    --    When enforced, the request model must be present and in the list.
    if type(conf.allowed_models) == "table" and #conf.allowed_models > 0 then
        local model = request_model()
        if not model or not list_has(conf.allowed_models, model) then
            return deny403("model_not_permitted", "Model not permitted for this API key")
        end
    end

    local key = consumer_id()

    -- 3) Concurrency: increment in-flight, remember it for log() to decrement.
    if conf.concurrency and tonumber(conf.concurrency) and tonumber(conf.concurrency) > 0 then
        local dict = counter_dict()
        if dict then
            local cc_key = "neutree_cc:" .. key
            local inflight = dict:incr(cc_key, 1, 0)
            kong.ctx.plugin.cc_key = cc_key
            if inflight and inflight > tonumber(conf.concurrency) then
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
                    -- Atomically initialize the window counter so two concurrent
                    -- first-requests don't both reset it to 1 (undercounting):
                    -- add() only succeeds for the first caller, then incr().
                    dict:add(rk, 0, wsec)
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
