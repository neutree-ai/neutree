local metrics = require("kong.plugins.neutree-metrics.metrics")

return {
    ["/metrics/neutree"] = { GET = function() return metrics.collect() end },
}
