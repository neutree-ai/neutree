-- One declaration per metric family. A source is optional for collectors that
-- only read request context. Collectors define init(registry), log(source) and
-- collect(source); unused lifecycle methods may be omitted.
return {
    {
        name = "model-routing",
        source = require("kong.plugins.neutree-ai-gateway.observation"),
        collector = require("kong.plugins.neutree-metrics.model_routing"),
    },
}
