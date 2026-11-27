-- neutree-ai-quota: per-consumer token-quota enforcement for an API key.
--
-- The token quota's *remaining* count is the only dynamic value, so unlike the
-- static access limits it is NOT reconciled onto the consumer; this plugin pulls
-- it from neutree-api (api.get_api_key_remaining) and caches it per worker.
--   remaining <= 0      -> 429 quota_exceeded
--   never fetched successfully -> allowed (FAIL-OPEN: prefer inference availability)
--
-- The cache is stale-while-revalidate (NEU-900): once an entry is older than
-- cache_ttl, the request is still decided by the cached value and a background
-- timer refreshes it, so a slow or failing control plane never sits on the
-- request path. A failed refresh keeps the last known value with no age limit:
-- during a database failover or outage a key keeps being limited by its last
-- balance instead of becoming unlimited. Only an entry that never fetched
-- successfully fails open, and it is retried in the background as well.
-- The plugin is attached only when the key has a token quota or a token pool
-- applies to its owner (a group's, or one set on the owner); the remaining count
-- is already the min of them all.
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
local lrucache = require("resty.lrucache")
local semaphore = require("ngx.semaphore")

local QuotaHandler = {
    PRIORITY = 890, -- below neutree-ai-access (895): 403 gating precedes 429 quota
    VERSION = "1.0.0",
}

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
--
-- Model-discovery requests (a model list) reach here with model = nil on purpose:
-- the access plugin lets them past its allowlist because they call no model, so
-- there is nothing for a per-model quota to charge and the control plane resolves
-- them as unlimited.
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

-- Returns the gate ({ remaining = n } or { unlimited = true }), or nil + err when
-- the control plane gave no usable answer. Runs in timers too, so it must not
-- touch request-scoped PDK APIs; it logs through ngx.log for the same reason.
local function fetch_remaining(conf, timeout, api_key_id, model, ep_type, ep_name)
    local httpc = http.new()
    httpc:set_timeout(timeout)

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

    if not res then
        return nil, "remaining fetch failed: " .. tostring(err)
    end

    if res.status ~= 200 then
        return nil, "remaining fetch status " .. tostring(res.status) ..
            " body " .. trunc_body(res.body)
    end

    -- null is a real answer: no quota applies to this key.
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
        return nil, "unparseable remaining: " .. trunc_body(body)
    end

    return { remaining = n }
end

-- Per-worker entries: { gate = <last good gate or nil>, checked_at = <last
-- attempt>, refreshing = <bool>, inflight = <semaphore while the first fetch
-- runs> }. Bounded by LRU so idle (key, model, endpoint) tuples age out; an
-- evicted entry just takes the first-fetch path again.
local CACHE_SIZE = 10000
local entries = assert(lrucache.new(CACHE_SIZE))

-- Runs one fetch and records it on the entry. A failure leaves the previous
-- gate in place.
local function fetch_into(entry, conf, timeout, api_key_id, model, ep_type, ep_name)
    local ok, gate, err = pcall(fetch_remaining, conf, timeout, api_key_id, model, ep_type, ep_name)
    entry.checked_at = ngx.now()
    if ok and gate then
        entry.gate = gate
        return
    end
    if not ok then
        err = gate
    end
    ngx.log(ngx.WARN, "neutree-ai-quota: ", tostring(err),
        entry.gate and " (keeping last known value)" or " (no value yet, allowing)")
end

-- A refresh is off the request path, so it can outwait a slow but live control
-- plane; with the request timeout it would keep timing out under PG latency and
-- the cached value would never move.
local REFRESH_TIMEOUT_MS = 10000

local function refresh(premature, entry, conf, api_key_id, model, ep_type, ep_name)
    if not premature then
        local timeout = math.max(conf.timeout or 2000, REFRESH_TIMEOUT_MS)
        fetch_into(entry, conf, timeout, api_key_id, model, ep_type, ep_name)
    end
    entry.refreshing = false
end

local function maybe_refresh(entry, ttl, conf, api_key_id, model, ep_type, ep_name)
    if entry.refreshing or ngx.now() - entry.checked_at < ttl then
        return
    end
    entry.refreshing = true
    local ok, err = ngx.timer.at(0, refresh, entry, conf, api_key_id, model, ep_type, ep_name)
    if not ok then
        entry.refreshing = false
        kong.log.err("neutree-ai-quota: cannot schedule refresh: ", err)
    end
end

-- Synchronous fetch for a tuple this worker has never seen. Concurrent first
-- requests for the same tuple wait on the one fetch rather than each calling
-- the control plane.
local function first_fetch(cache_key, conf, api_key_id, model, ep_type, ep_name)
    local entry = { checked_at = 0, refreshing = false, inflight = semaphore.new(), waiters = 0 }
    entries:set(cache_key, entry)

    fetch_into(entry, conf, conf.timeout or 2000, api_key_id, model, ep_type, ep_name)

    local sema = entry.inflight
    entry.inflight = nil
    if entry.waiters > 0 then
        sema:post(entry.waiters)
    end
    return entry
end

local function wait_first_fetch(entry, conf)
    entry.waiters = entry.waiters + 1
    -- Bounded by the fetch timeout plus slack; on timeout the caller sees no
    -- gate yet and fails open like the fetching request would.
    entry.inflight:wait(((conf.timeout or 2000) + 500) / 1000)
end

-- Length-prefixed cache-key segment: unambiguous for any byte content.
local function cache_part(v)
    v = v or ""
    return ":" .. #v .. ":" .. v
end

function QuotaHandler:access(conf)
    local consumer = kong.client.get_consumer()
    if not consumer or not consumer.custom_id or consumer.custom_id == "" then
        return
    end

    local api_key_id = consumer.custom_id
    local model, ep_type, ep_name = request_dimensions()
    -- Per-model quotas are tracked per (key, model, endpoint), so the cached
    -- remaining count must be scoped the same way. Length-prefix each part
    -- rather than joining on a separator: model names are only validated as
    -- non-empty strings and may contain ":", so a plain join would let
    -- ("a:internal", "", "") and ("a", "internal", "") share one entry and
    -- therefore one quota.
    local cache_key = "neutree_quota:" .. api_key_id ..
        cache_part(model) .. cache_part(ep_type) .. cache_part(ep_name)
    local ttl = conf.cache_ttl or 5

    local entry = entries:get(cache_key)
    if not entry then
        entry = first_fetch(cache_key, conf, api_key_id, model, ep_type, ep_name)
    elseif entry.inflight then
        wait_first_fetch(entry, conf)
    else
        maybe_refresh(entry, ttl, conf, api_key_id, model, ep_type, ep_name)
    end

    local gate = entry.gate
    if not gate or gate.unlimited then
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
