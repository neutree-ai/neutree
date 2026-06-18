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
            allowed_models = {
              type = "array",
              required = false,
              elements = { type = "string" },
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
