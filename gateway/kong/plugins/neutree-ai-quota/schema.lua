local typedefs = require("kong.db.schema.typedefs")

local PLUGIN_NAME = "neutree-ai-quota"

return {
  name = PLUGIN_NAME,
  fields = {
    { protocols = typedefs.protocols_http },
    {
      config = {
        type = "record",
        fields = {
          { api_url = { type = "string", required = true } },
          { service_token = { type = "string", required = true } },
          { cache_ttl = { type = "integer", default = 5 } },
          { timeout = { type = "integer", default = 2000 } },
        },
      },
    },
  },
}
