-- Central declaration of worker-local, read-only business state providers.
-- Add providers here; each provider owns its configuration and live state.
-- snapshot() returns { models = {...}, targets = {...} }; resolve(route_id,
-- path) optionally identifies requests rejected before the producer ran.
local gateway = require("kong.plugins.neutree-ai-gateway.observation")

return {
    ["model-routing"] = { snapshot = gateway.snapshot, resolve = gateway.resolve },
}
