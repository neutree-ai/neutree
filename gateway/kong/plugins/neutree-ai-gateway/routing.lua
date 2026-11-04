local M = {}
local health = require "kong.plugins.neutree-ai-gateway.health"

local function target_key(state, target)
    return table.concat({ state.scope or "", state.route.model, target.upstream, target.upstream_model }, "\0")
end

M.target_key = target_key

function M.configure(configs)
    local keys = {}
    for _, conf in ipairs(configs or {}) do
        for _, route in ipairs(conf.model_routes or {}) do
            if #route.targets > 1 then
                for _, target in ipairs(route.targets) do
                    keys[target_key({ scope = conf.route_prefix, route = route }, target)] = true
                end
            end
        end
    end
    health.configure(ngx.shared and ngx.shared.neutree_ai_gateway_inflight, keys)
end

local function candidates(state)
    local selected, priority
    for _, target in ipairs(state.route.targets or {}) do
        local key = target_key(state, target)
        if not state.tried[key] and not state.excluded[key] then
            local value = target.priority or 0
            if priority == nil or value < priority then
                selected, priority = {}, value
            end
            if value == priority then
                selected[#selected + 1] = target
            end
        end
    end
    return selected or {}
end

local function weighted_choice(targets)
    local total = 0
    for _, target in ipairs(targets) do
        total = total + (target.weight or 1)
    end
    local point = math.random() * total
    for _, target in ipairs(targets) do
        point = point - (target.weight or 1)
        if point < 0 then
            return target
        end
    end
    return targets[#targets]
end

local function capacity_lease(state, target)
    local maximum = target.max_inflight_requests or 0
    local shared = state.env and state.env.shared
    if not shared then
        return function() end
    end

    local key = target_key(state, target)
    local value, err = shared:incr(key, 1, 0)
    if not value then
        if maximum <= 0 then
            -- Counting an unlimited target is best effort; admission is unchanged.
            if kong then kong.log.err("routing concurrency observation failed: ", err) end
            return function() end
        end
        return nil, "counter_unavailable", err
    end
    if maximum > 0 and value > maximum then
        shared:incr(key, -1, value)
        return nil, "capacity_exhausted"
    end
    return function()
        shared:incr(key, -1, value)
    end
end

function M.begin(conf, model, env)
    for _, route in ipairs(conf.model_routes or {}) do
        if route.model == model then
            return {
                route = route,
                scope = conf.route_prefix,
                env = env or {},
                tried = {},
                excluded = {},
                attempts = 0,
                health_enabled = #route.targets > 1,
            }
        end
    end
    return nil, "model_not_found"
end

function M.next(state)
    local capacity_exhausted = false
    local health_unavailable = false
    while true do
        local available = candidates(state)
        if #available == 0 then
            return nil, health_unavailable and "targets_unavailable" or capacity_exhausted and "capacity_exhausted"
                or "no_available_target"
        end
        local target = weighted_choice(available)
        local key = target_key(state, target)
        local admission, health_err
        if state.health_enabled then admission, health_err = health.acquire(state.env.shared, key) end
        if state.health_enabled and not admission then
            if health_err ~= "unavailable" and health_err ~= "busy" then
                return nil, "health_unavailable", health_err
            end
            health_unavailable = true
        else
            local lease, err, detail = capacity_lease(state, target)
            if lease then
                state.current = target
                state.lease = lease
                state.health = admission
                state.tried[key] = true
                state.attempts = state.attempts + 1
                return target
            end
            -- Capacity rejection must not leave a half-open probe occupied.
            if admission then
                local ok, record_err = health.record(state.env.shared, admission, "neutral")
                if not ok then kong.log.err("unable to release model target probe: ", record_err) end
            end
            if err == "counter_unavailable" then
                return nil, err, detail
            end
            if err == "capacity_exhausted" then
                capacity_exhausted = true
            end
        end
        state.excluded[key] = true
    end
end

-- Call once when response headers identify the outcome, or from log for a
-- request that never got headers. Probe admission is independent of the live
-- inference concurrency lease, which remains held until finish().
function M.observe(state, outcome)
    if state.health and not state.health_observed then
        state.health_observed = true
        local ok, err = health.record(state.env.shared, state.health, outcome)
        if not ok then kong.log.err("unable to record model target health: ", err) end
    end
end

function M.finish(state)
    if state then M.observe(state, "neutral") end
    if state and state.lease then
        local lease = state.lease
        state.lease = nil
        lease()
    end
end

return M
