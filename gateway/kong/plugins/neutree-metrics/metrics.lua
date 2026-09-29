-- Shared lifecycle only: metric semantics belong to the declared collectors.
local collectors = require("kong.plugins.neutree-metrics.registry")
local exporter = require("kong.plugins.prometheus.exporter")
local M = {}
local initialized = {}

local function invoke(entry, owner, method, argument)
    local callback = entry[owner] and entry[owner][method]
    if not callback then return true end
    local ok, data, err = pcall(callback, argument)
    if not ok or err then
        kong.log.err("neutree metrics ", owner, " ", entry.name, " ", method, ": ", ok and err or data)
        return false
    end
    return true, data
end

local function dispatch(phase, registry)
    local ok = true
    for _, entry in ipairs(collectors) do
        if not initialized[entry] then
            initialized[entry] = invoke(entry, "collector", "init", registry)
        end
        if not initialized[entry] then
            ok = false
        elseif phase ~= "init" and entry.collector[phase] then
            local method = phase == "record" and "request" or "snapshot"
            local read_ok, data = invoke(entry, "source", method)
            -- A missing/failed snapshot must clear old gauges. Request counters
            -- simply skip absent facts. No business schema is imposed here.
            if phase == "collect" or data ~= nil then
                local write_ok = invoke(entry, "collector", phase, data)
                if not write_ok then ok = false end
            end
            if not read_ok then ok = false end
        end
    end
    return ok
end

function M.init()
    local registry = exporter.get_prometheus()
    return registry and dispatch("init", registry) or false
end

function M.log()
    if kong.ctx.plugin.metrics_recorded then return end
    local registry = exporter.get_prometheus()
    if not registry then return end
    -- Each collector gets one attempt, even if another collector fails.
    kong.ctx.plugin.metrics_recorded = true
    dispatch("record", registry)
end

function M.collect()
    local registry = exporter.get_prometheus()
    if not registry then
        return kong.response.exit(503, { message = "Metrics exporter is not initialized" })
    end
    dispatch("collect", registry)
    -- Export once after every collector has refreshed its metrics.
    local ok, result = pcall(exporter.collect)
    if ok then return result end
    kong.log.err("neutree metrics export failed: ", result)
    return kong.response.exit(503, { message = "Metrics export failed" })
end

return M
