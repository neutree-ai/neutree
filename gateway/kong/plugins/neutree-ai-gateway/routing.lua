local M = {}

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
            return nil, capacity_exhausted and "capacity_exhausted"
                or "no_available_target"
        end
        local target = weighted_choice(available)
        local key = target_key(state, target)
        local lease, err, detail = capacity_lease(state, target)
        if lease then
            state.current = target
            state.lease = lease
            state.tried[key] = true
            state.attempts = state.attempts + 1
            return target
        end
        if err == "counter_unavailable" then
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
