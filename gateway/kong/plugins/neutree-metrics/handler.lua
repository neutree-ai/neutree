local metrics = require("kong.plugins.neutree-metrics.metrics")

local Handler = {
    -- Run after gateway (900), access (895) and statistics/quota (890).
    PRIORITY = 880,
    VERSION = "0.1.0",
}

function Handler:init_worker()
    metrics.init()
end

function Handler:log()
    metrics.log()
end

return Handler
