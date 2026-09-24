local metrics = require("kong.plugins.neutree-ai-gateway.metrics")

return {
    ["/metrics/neutree"] = {
        GET = function()
            metrics.collect()
        end,
    },
}
