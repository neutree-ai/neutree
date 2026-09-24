local M = {}

-- Keep only facts from checks this request actually performed, with bounded
-- log size. Never serialize the target configuration (it contains credentials).
local MAX_SKIPPED_TARGETS = 32

local function target_observation(target, inflight, reason)
    return {
        upstream = target.upstream,
        upstream_model = target.upstream_model,
        priority = target.priority or 0,
        weight = target.weight or 1,
        max_inflight_requests = target.max_inflight_requests or 0,
        inflight = inflight,
        reason = reason,
    }
end

local function target_key(state, target)
    return table.concat({ state.scope or "", state.route.model, target.upstream, target.upstream_model }, "\0")
end

M.target_key = target_key

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
        return nil, "capacity_exhausted", nil, value - 1
    end
    return function()
        shared:incr(key, -1, value)
    end, nil, nil, value - 1
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
                decision = { result = "unassigned", skipped = {}, skipped_total = 0 },
            }
        end
    end
    return nil, "model_not_found"
end

function M.next(state)
    local capacity_exhausted = false
    while true do
        local available = candidates(state)
        if #available == 0 then
            local reason = capacity_exhausted and "capacity_exhausted" or "no_available_target"
            state.decision.reason = reason
            return nil, reason
        end
        local target = weighted_choice(available)
        local key = target_key(state, target)
        local lease, err, detail, inflight = capacity_lease(state, target)
        if lease then
            state.current = target
            state.lease = lease
            state.tried[key] = true
            state.attempts = state.attempts + 1
            state.decision.result = "selected"
            state.decision.reason = state.decision.skipped_total > 0 and "capacity_filtered" or "priority_weight"
            state.decision.selected = target_observation(target, inflight)
            return target
        end
        state.decision.skipped_total = state.decision.skipped_total + 1
        if #state.decision.skipped < MAX_SKIPPED_TARGETS then
            state.decision.skipped[#state.decision.skipped + 1] = target_observation(target, inflight, err)
        end
        if err == "counter_unavailable" then
            state.decision.reason = err
            return nil, err, detail
        end
        if err == "capacity_exhausted" then
            capacity_exhausted = true
        end
        state.excluded[key] = true
    end
end

function M.finish(state)
    if state and state.lease then
        local lease = state.lease
        state.lease = nil
        lease()
    end
end

return M
