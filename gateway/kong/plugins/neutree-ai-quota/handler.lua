-- neutree-ai-quota: per-consumer token-quota enforcement for an API key.
--
-- The token quota's *remaining* count is the only dynamic value, so unlike the
-- static access limits it is NOT reconciled onto the consumer; this plugin pulls
-- it from neutree-api
-- (api.get_api_key_remaining) at request time, cached briefly in kong.cache.
--   remaining <= 0      -> 429 quota_exceeded
--   fetch fails/uncertain -> allowed (FAIL-OPEN: prefer inference availability)
-- The plugin is attached only when the key has a token quota, so keys without a
-- quota are never blocked here.
--
-- A key's quota can be per-model rather than a single pool for the whole key, so
-- the remaining count is resolved against the model and the IE/EE endpoint this
-- request hit (stashed in kong.ctx.shared by neutree-ai-gateway, priority 1100 --
-- the same values neutree-ai-access uses for its allowlist). Which granularity
-- applies is decided by the control plane, not here: this plugin always sends the
-- request's dimensions and get_api_key_remaining ignores them for a key on an
-- overall quota. The cache key therefore MUST include those dimensions, or one
-- model exhausting its quota would start rejecting every other model on the key.

local http = require("resty.http")
local cjson = require("cjson.safe")

local QuotaHandler = {
    PRIORITY = 890, -- below neutree-ai-access (895): 403 gating precedes 429 quota
    VERSION = "1.0.0",
}

-- Returns a table so kong.cache can memoize:
--   { remaining = <number> }  -> enforce
--   { unlimited = true }      -> never block (no quota set)
-- Cap how much of an upstream body we log so a large/HTML error page can't spam
-- logs or leak details.
local MAX_LOG_BODY = 256
local function trunc_body(b)
    b = tostring(b)
    if #b > MAX_LOG_BODY then
        return string.sub(b, 1, MAX_LOG_BODY) .. "...(truncated)"
    end
    return b
end

local function str_or_nil(v)
    if type(v) == "string" and v ~= "" then
        return v
    end
    return nil
end

-- Request dimensions for the per-model lookup: the client-facing model plus the
-- IE/EE endpoint the request hit. Values are stashed by neutree-ai-gateway (1100)
-- ahead of its model-mapping rewrite; this plugin runs at 890, so they are
-- already in place.
--
-- The model resolution MUST match neutree-ai-access (895) exactly, including its
-- raw-body fallback for routes that have no neutree-ai-gateway plugin. A key on a
-- per-model quota necessarily has an allowed_models list (the limit hangs off its
-- entries), so the access plugin has already resolved a model for every request
-- that reaches here -- if it resolved one via the body and this plugin only read
-- the stash, the model would come back nil and the quota would silently fail open.
--
-- Preferring the stash over the body also keeps the name client-facing: once the
-- gateway plugin has rewritten the body, the model in it is the UPSTREAM name,
-- which is not what quotas are keyed by. Where no stash exists no rewrite
-- happened either, so the body still carries the client-facing name.
local function request_dimensions()
    local shared = kong.ctx.shared or {}
    local model = str_or_nil(shared.neutree_request_model)

    if not model then
        local raw = kong.request.get_raw_body()
        if raw and raw ~= "" then
            local decoded = cjson.decode(raw)
            if type(decoded) == "table" then
                model = str_or_nil(decoded.model)
            end
        end
    end

    return model,
           str_or_nil(shared.neutree_endpoint_type),
           str_or_nil(shared.neutree_endpoint_name)
end

local function fetch_remaining(conf, api_key_id, model, ep_type, ep_name)
    local httpc = http.new()
    httpc:set_timeout(conf.timeout or 2000)

    local res, err = httpc:request_uri(conf.api_url .. "/rpc/get_api_key_remaining", {
        method = "POST",
        body = cjson.encode({
            p_id       = api_key_id,
            p_model    = model    or cjson.null,
            p_type     = ep_type  or cjson.null,
            p_endpoint = ep_name  or cjson.null,
        }),
        headers = {
            ["Content-Type"]  = "application/json",
            ["Accept"]        = "application/json",
            ["Authorization"] = "Bearer " .. (conf.service_token or ""),
        },
    })

    -- Fail-open on any fetch problem and CACHE the decision (return a value, not
    -- an error) so kong.cache memoizes "unlimited" for cache_ttl instead of
    -- re-calling the control plane + logging on every request during an outage.
    if not res then
        kong.log.warn("neutree-ai-quota: remaining fetch failed: ", tostring(err))
        return { unlimited = true }
    end

    if res.status ~= 200 then
        kong.log.warn("neutree-ai-quota: remaining fetch status ", tostring(res.status),
            " body ", trunc_body(res.body))
        return { unlimited = true }
    end

    local body = res.body
    if not body or body == "" or body == "null" then
        return { unlimited = true }
    end

    local n = tonumber(body)
    if n == nil then
        local decoded = cjson.decode(body)
        n = tonumber(decoded)
    end

    if n == nil then
        kong.log.warn("neutree-ai-quota: unparseable remaining: ", trunc_body(body))
        return { unlimited = true }
    end

    return { remaining = n }
end

function QuotaHandler:access(conf)
    local consumer = kong.client.get_consumer()
    if not consumer or not consumer.custom_id or consumer.custom_id == "" then
        return
    end

    local api_key_id = consumer.custom_id
    local model, ep_type, ep_name = request_dimensions()
    -- Per-model quotas are tracked per (key, model, endpoint), so the cached
    -- remaining count must be scoped the same way.
    local cache_key = "neutree_quota:" .. api_key_id ..
        ":" .. (model or "") .. ":" .. (ep_type or "") .. ":" .. (ep_name or "")
    local ttl = conf.cache_ttl or 5

    local gate, err = kong.cache:get(cache_key, { ttl = ttl, neg_ttl = ttl },
        fetch_remaining, conf, api_key_id, model, ep_type, ep_name)
    if err then
        -- FAIL-OPEN: cannot determine remaining -> allow the request through,
        -- preferring inference availability over strict enforcement during a
        -- control-plane/DB outage.
        kong.log.err("neutree-ai-quota: ", err)
        return
    end

    if gate.unlimited then
        return
    end

    if gate.remaining ~= nil and gate.remaining <= 0 then
        return kong.response.exit(429, {
            error = {
                message = "Token quota exceeded for this API key",
                type = "quota_exceeded",
                code = "quota_exceeded",
            },
        })
    end
end

return QuotaHandler
