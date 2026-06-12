-- neutree-ai-quota: enforces the three-tier token quota at the AI gateway.
--
-- The management plane owns all quota semantics (workspace/user/api_key levels
-- across daily/weekly/monthly/yearly periods). The gateway only ever sees an
-- API key, so it consumes a single scalar: the minimum remaining tokens for
-- that key across every applicable policy, computed by
-- api.get_api_key_remaining and exposed through neutree-api /rpc.
--
-- This runs at a lower priority than key-auth (1003) so the authenticated
-- consumer (custom_id = api_key id) is available. The remaining value is cached
-- per key for a few seconds (kong.cache), so steady-state requests stay in
-- memory and a key may slightly overspend within one sync window -- the
-- "少量超额" the design explicitly accepts.

local http = require("resty.http")
local cjson = require("cjson.safe")

local QuotaHandler = {
    -- after key-auth (1003) so kong.client.get_consumer() is populated.
    PRIORITY = 890,
    VERSION = "0.0.1",
}

local function trim(s)
    return (string.gsub(s or "", "^%s*(.-)%s*$", "%1"))
end

-- Fetch the minimum remaining tokens for one API key from neutree-api.
-- Returns a table so kong.cache can store "unlimited" distinctly from a number:
--   { unlimited = true }      -> key has no quota / unconstrained -> never block
--   { remaining = <number> }  -> enforce when <= 0
local function fetch_remaining(conf, api_key_id)
    local httpc = http.new()
    httpc:set_timeout(conf.timeout or 2000)

    local res, err = httpc:request_uri(conf.api_url .. "/api/v1/rpc/get_api_key_remaining", {
        method = "POST",
        body = cjson.encode({ p_api_key_id = api_key_id }),
        headers = {
            ["Content-Type"]  = "application/json",
            ["Accept"]        = "application/json",
            ["Authorization"] = "Bearer " .. (conf.service_token or ""),
        },
    })

    if not res then
        return nil, "remaining fetch failed: " .. tostring(err)
    end
    if res.status ~= 200 then
        return nil, "remaining fetch status " .. tostring(res.status) .. " body " .. tostring(res.body)
    end

    local body = trim(res.body)
    if body == "" or body == "null" then
        return { unlimited = true }
    end

    local val = tonumber(body)
    if val == nil then
        local decoded = cjson.decode(res.body)
        if type(decoded) == "number" then
            val = decoded
        end
    end
    if val == nil then
        -- Unparseable response: treat as unconstrained rather than block traffic.
        return { unlimited = true }
    end

    return { remaining = val }
end

function QuotaHandler:access(conf)
    if not conf.service_token or conf.service_token == "" then
        return
    end

    local consumer = kong.client.get_consumer()
    if not consumer or not consumer.custom_id or consumer.custom_id == "" then
        -- No authenticated API key (e.g. anonymous / unmatched). Nothing to enforce.
        return
    end

    local api_key_id = consumer.custom_id
    local cache_key = "neutree_quota:" .. api_key_id
    local ttl = conf.cache_ttl or 5
    local opts = { ttl = ttl, neg_ttl = ttl }

    local entry, err = kong.cache:get(cache_key, opts, fetch_remaining, conf, api_key_id)
    if err then
        kong.log.warn("neutree-ai-quota: ", err)
        -- Fail open: do not block traffic when the management plane is unreachable.
        return
    end

    if entry and entry.remaining ~= nil and entry.remaining <= 0 then
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
