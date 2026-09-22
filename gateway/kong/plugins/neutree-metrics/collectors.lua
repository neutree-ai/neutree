-- Unified data protocol, one entry per metric family:
-- source.request() -> request facts in log phase; source.snapshot() -> live facts
-- at scrape time. Either method may be omitted. Return nil for no data,
-- nil, err (or throw) for failure. Sources never update metrics.
-- collector.init(registry), record(data), collect(data) may each be omitted.
-- record receives only available request data. collect receives nil on absent
-- or failed snapshots and MUST clear its previous gauges in that case.
-- Collectors consume data only: no source calls or Kong/ngx state reads.
-- Both sides are isolated per entry. Export happens once after collection.
-- New business: implement a source + collector, then add their pair here.
return {
    {
        name = "model-routing",
        source = require("kong.plugins.neutree-ai-gateway.model_routing"),
        collector = require("kong.plugins.neutree-metrics.collectors.model_routing"),
    },
}
