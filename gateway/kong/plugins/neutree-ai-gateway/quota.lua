-- Per-API-key token quota enforcement for the AI gateway (NEUTREE-GENERAL-9).
--
-- On each inference request the gateway looks up the requesting API key's
-- minimum remaining tokens -- api.get_api_key_remaining(), which the management
-- plane computes across every applicable workspace/user/api_key quota policy and
-- period -- and rejects with HTTP 429 when it is exhausted (<= 0).
--
-- The value is cached per API key for a few seconds (kong.cache), so the hot
-- path is a single in-memory lookup. This yields second-level sync with the
-- management plane; within a TTL window a key may run a small overage before the
-- refreshed value is observed, which is the accepted tradeoff in the design.
--
-- Failure modes fail OPEN (allow the request): a quota subsystem hiccup must not
-- take down inference. Keys with no quota policy are unconstrained.

local pgmoon = require("pgmoon")

local _M = {}

local CACHE_TTL = 5                       -- seconds
local UNLIMITED = 9.2e18                  -- sentinel: effectively no limit

-- Query the management-plane remaining-token function for one API key.
-- Returns a number (may be negative when already over quota) or UNLIMITED.
local function load_remaining(api_key_id)
    local cfg = kong.configuration
    local pg = pgmoon.new({
        host        = cfg.pg_host,
        port        = cfg.pg_port or 5432,
        database    = cfg.pg_database,
        user        = cfg.pg_user,
        password    = cfg.pg_password,
        socket_type = "nginx",
    })

    local ok, err = pg:connect()
    if not ok then
        kong.log.err("[quota] pg connect failed: ", err)
        return UNLIMITED                  -- fail open
    end

    local sql = "SELECT api.get_api_key_remaining(" ..
                pg:escape_literal(api_key_id) .. "::uuid) AS r"
    local res, qerr = pg:query(sql)
    pg:keepalive()

    if not res then
        kong.log.err("[quota] remaining query failed: ", qerr)
        return UNLIMITED                  -- fail open
    end

    local r = res[1] and res[1].r
    if r == nil then
        return UNLIMITED                  -- no policy -> unconstrained
    end
    return tonumber(r)
end

-- Enforce quota for the current request. Returns nil normally; on denial it
-- calls kong.response.exit (which terminates the request and does not return).
function _M.enforce()
    local consumer = kong.client.get_consumer()
    local api_key_id = consumer and consumer.custom_id
    if not api_key_id then
        return                            -- no resolved API key: leave to auth
    end

    local remaining, err = kong.cache:get(
        "neutree_quota:" .. api_key_id,
        { ttl = CACHE_TTL, neg_ttl = CACHE_TTL },
        load_remaining, api_key_id)

    if err then
        kong.log.err("[quota] cache error: ", err)
        return                            -- fail open
    end

    if remaining and remaining ~= UNLIMITED and remaining <= 0 then
        return kong.response.exit(429, {
            error = {
                message = "Token quota exceeded for this API key. " ..
                          "Contact your administrator or wait for the next quota period.",
                type = "insufficient_quota",
                code = "quota_exceeded",
            },
        })
    end
end

return _M
