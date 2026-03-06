local PLUGIN_NAME = "neutree-model-router"

local schema = {
  name = PLUGIN_NAME,
  fields = {
    { config = {
        type = "record",
        fields = {
          {
            route_prefix = {
              type = "string",
              required = true,
            },
          },
          {
            upstreams = {
              type = "array",
              required = true,
              elements = {
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
                },
              },
            },
          },
        },
      },
    },
  },
}

return schema
