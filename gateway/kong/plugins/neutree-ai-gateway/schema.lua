local PLUGIN_NAME = "neutree-ai-gateway"

local upstream_entry = {
  type = "record",
  fields = {
    {
      scheme = {
        type = "string",
        required = true,
        one_of = { "http", "https" },
      },
    },
    {
      host = {
        type = "string",
        required = true,
      },
    },
    {
      port = {
        type = "integer",
        required = true,
      },
    },
    {
      path = {
        type = "string",
        required = true,
        default = "/",
      },
    },
    {
      auth_header = {
        type = "string",
        required = false,
      },
    },
    {
      model_mapping = {
        type = "map",
        required = true,
        keys = { type = "string" },
        values = { type = "string" },
      },
    },
    {
      internal = {
        type = "boolean",
        required = false,
        default = false,
      },
    },
  },
}

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
          {
            route_prefix = {
              type = "string",
              required = false,
            },
          },
          {
            upstreams = {
              type = "array",
              required = false,
              elements = upstream_entry,
            },
          },
        },
      },
    },
  },
}

return schema
