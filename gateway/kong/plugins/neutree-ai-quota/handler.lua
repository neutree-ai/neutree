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

local http = require("resty.http")
local cjson = require("cjson.safe")

local QuotaHandler = {
    PRIORITY = 890, -- below neutree-ai-access (895): 403 gating precedes 429 quota
    VERSION = "1.0.0",
}

-- Returns a table so kong.cache can memoize:
--   { remaining = <number> }  -> enforce
--   { unlimited = true }      -> never block (no quota set)
local function fetch_remaining(conf, api_key_id)
    local httpc = http.new()
    httpc:set_timeout(conf.timeout or 2000)

    local res, err = httpc:request_uri(conf.api_url .. "/rpc/get_api_key_remaining", {
        method = "POST",
        body = cjson.encode({ p_id = api_key_id }),
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
            " body ", tostring(res.body))
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
        kong.log.warn("neutree-ai-quota: unparseable remaining: ", tostring(body))
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
    local cache_key = "neutree_quota:" .. api_key_id
    local ttl = conf.cache_ttl or 5

    local gate, err = kong.cache:get(cache_key, { ttl = ttl, neg_ttl = ttl }, fetch_remaining, conf, api_key_id)
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
