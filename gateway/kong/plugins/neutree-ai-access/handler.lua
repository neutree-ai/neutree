-- neutree-ai-access: enforces access-control policies at the AI gateway.
--
-- Sibling of neutree-ai-quota. Quota is a cumulative budget (429 quota_exceeded);
-- access is per-request gating / short-window rate limiting. The management plane
-- owns all access semantics across workspace/user/api_key levels and resolves the
-- MOST RESTRICTIVE applicable rule into a single object the gateway consumes via
-- api.get_api_key_access (exposed through neutree-api /rpc).
--
-- Enforced rules:
--   * model_allowlist / endpoint_allowlist -> 403 not_permitted when the request
--                    targets a model/endpoint outside the (intersected) allowlist.
--   * rate_limit  -> fixed-window counter per (key, window) in a lua_shared_dict
--                    (per-node; "暂时 lua", no Redis), windows second/minute/hour/day,
--                    429 rate_limited.
--   * concurrency -> in-flight counter incremented here, decremented in log().
-- (ip/header allowlist rule types are storable but not enforced yet.)
--
-- Runs after key-auth (1003) so the consumer (custom_id = api_key id) is known,
-- and slightly ABOVE neutree-ai-quota (890) so a 403/429 access rejection
-- precedes a 429 quota rejection.

local http = require("resty.http")
local cjson = require("cjson.safe")

local AccessHandler = {
    PRIORITY = 895,
    VERSION = "0.0.3",
}

local WINDOW_SECONDS = { second = 1, minute = 60, hour = 3600, day = 86400 }

-- Derive the request's target: the model (OpenAI-style request body) and the
-- endpoint (route path /workspace/<ws>/endpoint/<name> or .../external-endpoint/
-- <name>). Best-effort; anything missing stays nil and simply does not trigger
-- an allowlist rejection. Mirrors neutree-ai-quota's request_dimension().
local function request_target()
    local etype, ename
    local path = kong.request.get_path() or ""
    ename = path:match("/external%-endpoint/([^/]+)")
    if ename then
        etype = "external_endpoint"
    else
        ename = path:match("/endpoint/([^/]+)")
        if ename then
            etype = "endpoint"
        end
    end

    local model
    local raw = kong.request.get_raw_body()
    if raw and raw ~= "" then
        local decoded = cjson.decode(raw)
        if type(decoded) == "table" and type(decoded.model) == "string" then
            model = decoded.model
        end
    end
    return model, ename, etype
end

local function list_has(list, value)
    for _, v in ipairs(list) do
        if v == value then
            return true
        end
    end
    return false
end

local function endpoint_allowed(list, etype, ename)
    for _, e in ipairs(list) do
        if type(e) == "table" and e.type == etype and e.name == ename then
            return true
        end
    end
    return false
end

-- Reuse Kong's built-in rate-limiting shared dict so no extra nginx config is
-- required to deploy. Fall back to fail-open if it is unavailable.
local function counter_dict()
    return ngx.shared.kong_rate_limiting_counters or ngx.shared.kong_locks
end

-- Fetch the resolved access gate for one API key from neutree-api. Returns a
-- table so kong.cache can store "unlimited" distinctly:
--   { unlimited = true }                         -> no access policy -> never block
--   { rate_limits = {...}, concurrency = n|nil } -> enforce
local function fetch_access(conf, api_key_id)
    local httpc = http.new()
    httpc:set_timeout(conf.timeout or 2000)

    local res, err = httpc:request_uri(conf.api_url .. "/api/v1/rpc/get_api_key_access", {
        method = "POST",
        body = cjson.encode({ p_api_key_id = api_key_id }),
        headers = {
            ["Content-Type"]  = "application/json",
            ["Accept"]        = "application/json",
            ["Authorization"] = "Bearer " .. (conf.service_token or ""),
        },
    })

    if not res then
        return nil, "access fetch failed: " .. tostring(err)
    end
    if res.status ~= 200 then
        return nil, "access fetch status " .. tostring(res.status) .. " body " .. tostring(res.body)
    end

    local body = res.body
    if not body or body == "" or body == "null" then
        return { unlimited = true }
    end

    local decoded = cjson.decode(body)
    if type(decoded) ~= "table" then
        -- Unparseable: treat as unconstrained rather than block traffic.
        return { unlimited = true }
    end

    -- allowed_models / allowed_endpoints are JSON arrays when an allowlist is in
    -- effect, JSON null (cjson.null, not nil) when unrestricted. Keep only real
    -- arrays (type "table") so the handler can treat null/absent as unrestricted.
    local allowed_models = decoded.allowed_models
    if type(allowed_models) ~= "table" then allowed_models = nil end
    local allowed_endpoints = decoded.allowed_endpoints
    if type(allowed_endpoints) ~= "table" then allowed_endpoints = nil end

    return {
        disabled = decoded.disabled == true,
        rate_limits = decoded.rate_limits or {},
        concurrency = decoded.concurrency,
        allowed_models = allowed_models,
        allowed_endpoints = allowed_endpoints,
    }
end

-- Enforce every rate_limit window via a fixed-window counter. Returns
-- retry_after (seconds) when a window is exceeded, otherwise nil.
local function check_rate_limits(api_key_id, rate_limits)
    if type(rate_limits) ~= "table" or #rate_limits == 0 then
        return nil
    end
    local dict = counter_dict()
    if not dict then
        return nil -- fail open: no usable shared dict
    end

    local now = ngx.now()
    for _, rl in ipairs(rate_limits) do
        local limit = tonumber(rl.limit)
        local wsec = WINDOW_SECONDS[rl.window]
        if limit and wsec then
            local window_start = math.floor(now / wsec) * wsec
            local key = "neutree_rl:" .. api_key_id .. ":" .. rl.window .. ":" .. window_start
            local newval, err = dict:incr(key, 1)
            if not newval then
                -- key absent: seed it with this request and the window TTL.
                dict:set(key, 1, wsec)
                newval = 1
                if err then
                    kong.log.warn("neutree-ai-access: counter incr: ", err)
                end
            end
            if newval > limit then
                return math.max(1, math.ceil(window_start + wsec - now))
            end
        end
    end
    return nil
end

function AccessHandler:access(conf)
    if not conf.service_token or conf.service_token == "" then
        return
    end

    local consumer = kong.client.get_consumer()
    if not consumer or not consumer.custom_id or consumer.custom_id == "" then
        return
    end
    local api_key_id = consumer.custom_id

    local cache_key = "neutree_access:" .. api_key_id
    local ttl = conf.cache_ttl or 5
    local opts = { ttl = ttl, neg_ttl = ttl }

    local gate, err = kong.cache:get(cache_key, opts, fetch_access, conf, api_key_id)
    if err then
        kong.log.warn("neutree-ai-access: ", err)
        return -- fail open
    end
    if not gate or gate.unlimited then
        return
    end

    -- Disabled key: reject every request (403) before any other check. The
    -- management plane sets/clears this; clearing restores the key unchanged.
    if gate.disabled then
        return kong.response.exit(403, {
            error = {
                message = "This API key is disabled",
                type = "not_permitted",
                code = "key_disabled",
            },
        })
    end

    -- Allowlists (403 not_permitted) are checked before the 429 rate/concurrency
    -- limits. A null/absent allowlist is unrestricted; an empty array denies all.
    if gate.allowed_models or gate.allowed_endpoints then
        local model, endpoint, endpoint_type = request_target()
        if gate.allowed_models and model and not list_has(gate.allowed_models, model) then
            return kong.response.exit(403, {
                error = {
                    message = "Model not permitted for this API key",
                    type = "not_permitted",
                    code = "model_not_permitted",
                },
            })
        end
        if gate.allowed_endpoints and endpoint
            and not endpoint_allowed(gate.allowed_endpoints, endpoint_type, endpoint) then
            return kong.response.exit(403, {
                error = {
                    message = "Endpoint not permitted for this API key",
                    type = "not_permitted",
                    code = "endpoint_not_permitted",
                },
            })
        end
    end

    -- Concurrency: increment in-flight, remember it for log() to decrement.
    if gate.concurrency and tonumber(gate.concurrency) then
        local dict = counter_dict()
        if dict then
            local key = "neutree_cc:" .. api_key_id
            local inflight = dict:incr(key, 1, 0)
            kong.ctx.plugin.cc_key = key
            if inflight and inflight > tonumber(gate.concurrency) then
                dict:incr(key, -1)
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

    local retry_after = check_rate_limits(api_key_id, gate.rate_limits)
    if retry_after then
        -- release the concurrency slot we took, since the request is rejected.
        if kong.ctx.plugin.cc_key then
            local dict = counter_dict()
            if dict then dict:incr(kong.ctx.plugin.cc_key, -1) end
            kong.ctx.plugin.cc_key = nil
        end
        kong.response.set_header("Retry-After", retry_after)
        return kong.response.exit(429, {
            error = {
                message = "Request rate limit exceeded for this API key",
                type = "rate_limited",
                code = "rate_limit_exceeded",
            },
        })
    end
end

-- Release the concurrency slot when the request finishes.
function AccessHandler:log(conf)
    local key = kong.ctx.plugin.cc_key
    if key then
        local dict = counter_dict()
        if dict then dict:incr(key, -1) end
    end
end

return AccessHandler
