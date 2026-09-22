-- Shared lifecycle only: metric semantics belong to the declared collectors.
local collectors = require("kong.plugins.neutree-metrics.collectors")
local exporter = require("kong.plugins.prometheus.exporter")
local M = {}
local initialized = {}

local function invoke(entry, phase, argument)
    local callback = entry.collector[phase]
    if not callback then return true end
    local ok, err = pcall(callback, argument)
    if not ok then
        kong.log.err("neutree metrics collector ", entry.name, " ", phase, ": ", err)
    end
    return ok
end

local function dispatch(phase, registry)
    local ok = true
    for _, entry in ipairs(collectors) do
        if not initialized[entry] then
            initialized[entry] = invoke(entry, "init", registry)
        end
        if not initialized[entry] then
            ok = false
        elseif phase ~= "init" and not invoke(entry, phase, entry.source) then
            ok = false
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
    dispatch("log", registry)
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
