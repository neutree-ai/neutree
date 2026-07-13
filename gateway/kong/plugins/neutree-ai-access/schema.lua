local typedefs = require("kong.db.schema.typedefs")

local PLUGIN_NAME = "neutree-ai-access"

return {
  name = PLUGIN_NAME,
  fields = {
    { protocols = typedefs.protocols_http },
    {
      config = {
        type = "record",
        fields = {
          { disabled = { type = "boolean", default = false } },
          {
            -- Endpoint-scoped model allowlist. Each entry is { model, type?,
            -- endpoint_name? }: a request is permitted when its client-facing model
            -- equals `model` AND, for each of `type` / `endpoint_name` that is set,
            -- the endpoint the request hit (from kong.ctx.shared, stashed by
            -- neutree-ai-gateway) matches. Empty type/endpoint_name = any endpoint
            -- serving this model (legacy name-only semantics). An empty array = deny-all.
            allowed_models = {
              type = "array",
              required = false,
              elements = {
                type = "record",
                fields = {
                  { model = { type = "string", required = true } },
                  { type = { type = "string", required = false } },
                  { endpoint_name = { type = "string", required = false } },
                },
              },
            },
          },
          { concurrency = { type = "integer", required = false } },
          {
            rate_limits = {
              type = "array",
              required = false,
              elements = {
                type = "record",
                fields = {
                  { limit = { type = "integer", required = true } },
                  {
                    window = {
                      type = "string",
                      required = true,
                      one_of = { "second", "minute", "hour", "day" },
                    },
                  },
                },
              },
            },
          },
        },
      },
    },
  },
}
