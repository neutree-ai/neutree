local PLUGIN_NAME = "neutree-ai-statistics"

local schema = {
  name = PLUGIN_NAME,
  fields = {
    { config = {
        type = "record",
        fields = {
          {
            route_type = {
              type = "string",
              required = false,
            },
          },
        },
      },
    },
  }
}

return schema